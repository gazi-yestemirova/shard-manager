// Package drainedshardscache holds an in-memory mirror of the etcd
// drained-shards prefix for each namespace. A single goroutine per namespace
// owns the etcd watch and applies events to the local set; subscribers receive
// the full snapshot via pubsub fan-out and the hot path
// (handler.isShardDrained, ephemeral assignment) reads the same underlying set
// directly via Contains.
//
// The design mirrors the shardcache package but is intentionally simpler:
// drained state is just a set of keys, so events can be applied
// incrementally without re-fetching the whole prefix on every change.
package drainedshardscache

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/cadence-workflow/shard-manager/common/clock"
	"github.com/cadence-workflow/shard-manager/common/log"
	"github.com/cadence-workflow/shard-manager/common/log/tag"
	"github.com/cadence-workflow/shard-manager/common/metrics"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/store/etcd/etcdclient"
)

// Cache is the top-level drained-shards cache shared across namespaces. It
// lazily creates a per-namespace cache on first reference, each of which owns
// a single etcd watch and fans out updates to subscribers.
type Cache struct {
	mu            sync.RWMutex
	namespaces    map[string]*namespaceCache
	prefix        string
	client        etcdclient.Client
	logger        log.Logger
	timeSource    clock.TimeSource
	metricsClient metrics.Client
	stopC         chan struct{}
	wg            *sync.WaitGroup
}

// NewCache wires the dependencies needed by per-namespace caches. Use Start
// from the owning store's lifecycle hook before calling Subscribe/Contains.
func NewCache(
	prefix string,
	client etcdclient.Client,
	logger log.Logger,
	timeSource clock.TimeSource,
	metricsClient metrics.Client,
) *Cache {
	return &Cache{
		namespaces:    make(map[string]*namespaceCache),
		prefix:        prefix,
		client:        client,
		logger:        logger,
		timeSource:    timeSource,
		metricsClient: metricsClient,
		stopC:         make(chan struct{}),
		wg:            &sync.WaitGroup{},
	}
}

// Start is currently a no-op; per-namespace caches start lazily on first
// reference. The method exists so the cache can hang off the same fx
// StartStopHook as ShardToExecutorCache.
func (c *Cache) Start() {}

// Stop signals all per-namespace watch goroutines to exit and waits for them.
// Called from the store's fx Stop hook.
func (c *Cache) Stop() {
	close(c.stopC)
	c.wg.Wait()
}

// Subscribe registers a subscriber non-blockingly. The returned channel's
// first message is the current drained-shard snapshot (warm seed if the
// cache is already ready, otherwise the watcher's first publish), followed
// by every subsequent change
func (c *Cache) Subscribe(_ context.Context, namespace string) (<-chan []string, func(), error) {
	ns, err := c.getOrCreate(namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("get drained shards namespace cache: %w", err)
	}
	ch, unsub := ns.subscribe()
	return ch, unsub, nil
}

// Contains reports whether a shard key is currently drained for the namespace.
// `ready` indicates whether the per-namespace cache has received its first
// snapshot. Callers on the hot path (e.g. handler.isShardDrained) should fall
// back to a point read when `ready` is false to avoid serving stale negatives
// during the brief cold-start window.
func (c *Cache) Contains(namespace, shardKey string) (drained, ready bool) {
	ns, err := c.getOrCreate(namespace)
	if err != nil {
		// getOrCreate failure means we couldn't even start the watcher; report
		// not-ready so callers fall back to the point read.
		c.logger.Error("drainedshardscache: failed to create namespace cache", tag.Error(err))
		return false, false
	}
	return ns.contains(shardKey)
}

// Snapshot returns a copy of the drained-shard set; ready is
// false until the watcher has applied its first snapshot, mirroring
// Contains semantics.
func (c *Cache) Snapshot(namespace string) (set map[string]struct{}, ready bool) {
	ns, err := c.getOrCreate(namespace)
	if err != nil {
		c.logger.Error("drainedshardscache: failed to create namespace cache", tag.Error(err))
		return nil, false
	}
	return ns.snapshotSet()
}

func (c *Cache) getOrCreate(namespace string) (*namespaceCache, error) {
	c.mu.RLock()
	ns, ok := c.namespaces[namespace]
	c.mu.RUnlock()
	if ok {
		return ns, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if ns, ok := c.namespaces[namespace]; ok {
		return ns, nil
	}

	ns = newNamespaceCache(c.prefix, namespace, c.client, c.stopC, c.logger, c.timeSource, c.metricsClient)
	ns.start(c.wg)
	c.namespaces[namespace] = ns
	return ns, nil
}

// snapshotFromSet returns a sorted slice copy of the set so subscribers can
// rely on stable ordering for diffing without locking the cache.
func snapshotFromSet(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
