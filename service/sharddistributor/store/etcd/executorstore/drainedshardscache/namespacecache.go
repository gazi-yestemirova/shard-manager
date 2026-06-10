package drainedshardscache

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/cadence-workflow/shard-manager/common/backoff"
	"github.com/cadence-workflow/shard-manager/common/clock"
	"github.com/cadence-workflow/shard-manager/common/log"
	"github.com/cadence-workflow/shard-manager/common/log/tag"
	"github.com/cadence-workflow/shard-manager/common/metrics"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/store/etcd/etcdclient"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/store/etcd/etcdkeys"
)

const (
	// watchRetryInterval matches the existing executor cache, so reconnect
	// behaviour is consistent across the two watchers.
	watchRetryInterval = 100 * time.Millisecond
	watchJitterCoeff   = 0.5

	watchTypeTagValue = "drained_shards_cache"
)

// namespaceCache owns the etcd watch and in-memory drained-shard set for one
// namespace.
type namespaceCache struct {
	mu    sync.RWMutex // protects set
	set   map[string]struct{}
	ready atomic.Bool

	namespace     string
	etcdPrefix    string
	stopCh        chan struct{}
	logger        log.Logger
	client        etcdclient.Client
	timeSource    clock.TimeSource
	metricsClient metrics.Client

	pubSub *pubSub
}

func newNamespaceCache(
	etcdPrefix, namespace string,
	client etcdclient.Client,
	stopCh chan struct{},
	logger log.Logger,
	timeSource clock.TimeSource,
	metricsClient metrics.Client,
) *namespaceCache {
	return &namespaceCache{
		set:           make(map[string]struct{}),
		namespace:     namespace,
		etcdPrefix:    etcdPrefix,
		stopCh:        stopCh,
		logger:        logger.WithTags(tag.ShardNamespace(namespace)),
		client:        client,
		timeSource:    timeSource,
		metricsClient: metricsClient,
		pubSub:        newPubSub(logger, namespace),
	}
}

func (n *namespaceCache) start(wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		n.refreshLoop()
	}()
}

// contains reports membership; ready is false until the first successful
// snapshot lands, so callers know to fall back to a point read.
func (n *namespaceCache) contains(shardKey string) (drained, ready bool) {
	if !n.ready.Load() {
		return false, false
	}
	n.mu.RLock()
	_, drained = n.set[shardKey]
	n.mu.RUnlock()
	return drained, true
}

// subscribe seeds the current snapshot synchronously and streams every
// subsequent update. Slow consumers see coalesced updates, never stale ones.
func (n *namespaceCache) subscribe() (<-chan []string, func()) {
	return n.pubSub.subscribe(n.snapshot())
}

func (n *namespaceCache) snapshot() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return snapshotFromSet(n.set)
}

// snapshotSet returns a copy of the drained-shard set; ready is
// false until the first watch snapshot has landed.
func (n *namespaceCache) snapshotSet() (map[string]struct{}, bool) {
	if !n.ready.Load() {
		return nil, false
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make(map[string]struct{}, len(n.set))
	for k := range n.set {
		out[k] = struct{}{}
	}
	return out, true
}

// refreshLoop is the long-running goroutine that snapshots the prefix and
// applies subsequent watch events. On any error it backs off and retries; the
// in-memory set is preserved across reconnects so the hot path keeps serving
// from the previous snapshot.
func (n *namespaceCache) refreshLoop() {
	for {
		select {
		case <-n.stopCh:
			return
		default:
		}

		if err := n.snapshotAndWatch(); err != nil {
			n.logger.Error("drained shards refresh loop hit error, retrying", tag.Error(err))
			n.timeSource.Sleep(backoff.JitDuration(watchRetryInterval, watchJitterCoeff))
			continue
		}

		// snapshotAndWatch returns nil only when stopCh has fired.
		return
	}
}

func (n *namespaceCache) snapshotAndWatch() error {
	scope := n.metricsClient.Scope(metrics.ShardDistributorWatchScope).
		Tagged(metrics.NamespaceTag(n.namespace)).
		Tagged(metrics.ShardDistributorWatchTypeTag(watchTypeTagValue))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	revision, err := n.applyInitialSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("apply initial snapshot: %w", err)
	}

	prefix := etcdkeys.BuildDrainedShardsPrefix(n.etcdPrefix, n.namespace)
	watchCh := n.client.Watch(
		clientv3.WithRequireLeader(ctx),
		prefix,
		clientv3.WithPrefix(),
		clientv3.WithRev(revision+1),
	)

	for {
		select {
		case <-n.stopCh:
			return nil
		case watchResp, ok := <-watchCh:
			if !ok {
				return fmt.Errorf("drained shards watch channel closed")
			}
			if err := watchResp.Err(); err != nil {
				return fmt.Errorf("drained shards watch error: %w", err)
			}

			sw := scope.StartTimer(metrics.ShardDistributorWatchProcessingLatency)
			scope.AddCounter(metrics.ShardDistributorWatchEventsReceived, int64(len(watchResp.Events)))

			changed := n.applyEvents(watchResp.Events)
			if changed {
				n.pubSub.publish(n.snapshot())
			}
			sw.Stop()
		}
	}
}

// applyInitialSnapshot replaces the in-memory set wholesale with whatever the
// drained-shards prefix contains right now and returns the etcd revision used
// for the subsequent watch.
func (n *namespaceCache) applyInitialSnapshot(ctx context.Context) (int64, error) {
	prefix := etcdkeys.BuildDrainedShardsPrefix(n.etcdPrefix, n.namespace)
	resp, err := n.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return 0, fmt.Errorf("get drained shards prefix: %w", err)
	}

	fresh := make(map[string]struct{}, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		shardID, err := etcdkeys.ParseDrainedShardKey(n.etcdPrefix, n.namespace, string(kv.Key))
		if err != nil {
			n.logger.Warn("drained shards: skipping unparseable key", tag.Value(string(kv.Key)), tag.Error(err))
			continue
		}
		fresh[shardID] = struct{}{}
	}

	n.mu.Lock()
	n.set = fresh
	n.mu.Unlock()
	n.ready.Store(true)

	n.pubSub.publish(n.snapshot())

	return resp.Header.Revision, nil
}

// applyEvents incrementally updates the set based on watch events. It returns
// true when at least one event actually changed membership, so the caller can
// skip publishing on no-op events.
func (n *namespaceCache) applyEvents(events []*clientv3.Event) bool {
	if len(events) == 0 {
		return false
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	changed := false
	for _, ev := range events {
		shardID, err := etcdkeys.ParseDrainedShardKey(n.etcdPrefix, n.namespace, string(ev.Kv.Key))
		if err != nil {
			n.logger.Warn("drained shards: ignoring event with unparseable key", tag.Value(string(ev.Kv.Key)), tag.Error(err))
			continue
		}

		switch ev.Type {
		case clientv3.EventTypePut:
			if _, exists := n.set[shardID]; !exists {
				n.set[shardID] = struct{}{}
				changed = true
			}
		case clientv3.EventTypeDelete:
			if _, exists := n.set[shardID]; exists {
				delete(n.set, shardID)
				changed = true
			}
		default:
			n.logger.Warn("drained shards: ignoring event with unknown type", tag.Value(ev.Type.String()))
		}
	}
	return changed
}
