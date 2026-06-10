package drainedshardscache

import (
	"sync"

	"github.com/google/uuid"

	"github.com/cadence-workflow/shard-manager/common/log"
	"github.com/cadence-workflow/shard-manager/common/log/tag"
)

// pubSub fans out drained-shard snapshots to subscribers. Like the executor
// state pubsub it is non-blocking on publish: slow consumers drop updates
// rather than back-pressuring the watch goroutine.
//
// The full snapshot is sent on every publish so consumers can rebuild their
// drained set wholesale. This avoids the complexity of reconciling deltas
// across reconnects and missed updates.
type pubSub struct {
	mu          sync.RWMutex
	subscribers map[string]chan<- []string
	logger      log.Logger
	namespace   string
}

func newPubSub(logger log.Logger, namespace string) *pubSub {
	return &pubSub{
		subscribers: make(map[string]chan<- []string),
		logger:      logger,
		namespace:   namespace,
	}
}

// subscribe returns the receive channel and an idempotent unsubscribe func.
// The channel is unbuffered; non-blocking publishes drop updates if the
// consumer is not ready.
func (p *pubSub) subscribe() (chan []string, func()) {
	ch := make(chan []string)
	id := uuid.New().String()

	p.mu.Lock()
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
		select {
		case ch <- snapshot:
		default:
			p.logger.Warn("drained shards subscriber not keeping up, dropping update", tag.ShardNamespace(p.namespace))
		}
	}
}
