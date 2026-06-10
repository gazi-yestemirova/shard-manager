package drainedshardscache

import (
	"sync"

	"github.com/google/uuid"

	"github.com/cadence-workflow/shard-manager/common/log"
	"github.com/cadence-workflow/shard-manager/common/log/tag"
)

// pubSub fans out drained-shard snapshots to size-1 buffered subscribers.
// subscribe seeds the initial snapshot under p.mu so it is ordered before
// any later publish; publish coalesces against the buffer (drain-then-push)
// so the latest snapshot always wins and slow consumers never get pinned to
// stale state. Single publisher per namespace (the watch goroutine).
type pubSub struct {
	mu          sync.RWMutex
	subscribers map[string]chan []string
	logger      log.Logger
	namespace   string
}

func newPubSub(logger log.Logger, namespace string) *pubSub {
	return &pubSub{
		subscribers: make(map[string]chan []string),
		logger:      logger,
		namespace:   namespace,
	}
}

// subscribe registers a subscriber, seeds it with `initial` under p.mu, and
// returns the channel plus an idempotent unsubscribe func.
func (p *pubSub) subscribe(initial []string) (<-chan []string, func()) {
	ch := make(chan []string, 1)
	id := uuid.New().String()

	p.mu.Lock()
	ch <- initial
	p.subscribers[id] = ch
	p.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.subscribers, id)
			p.mu.Unlock()
			close(ch)
		})
	}
}

func (p *pubSub) publish(snapshot []string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, ch := range p.subscribers {
		// Drain any stale snapshot, then push the latest (coalesce).
		select {
		case <-ch:
			p.logger.Debug("drained shards subscriber missed an intermediate snapshot, coalescing to latest", tag.ShardNamespace(p.namespace))
		default:
		}
		select {
		case ch <- snapshot:
		default:
			// Unreachable under single-publisher locking; logged so we notice if it isn't.
			p.logger.Warn("drained shards: latest snapshot deferred, will retry on next publish", tag.ShardNamespace(p.namespace))
		}
	}
}
