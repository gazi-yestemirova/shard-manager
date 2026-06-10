package drainedshardscache

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/cadence-workflow/shard-manager/common/log/testlogger"
)

func seedFromSlice(s []string) func() []string {
	return func() []string { return s }
}

func TestPubSub_SubscribeSeedsInitialSnapshot(t *testing.T) {
	defer goleak.VerifyNone(t)

	ps := newPubSub(testlogger.New(t), "ns")
	initial := []string{"shard-1", "shard-2"}

	ch, unsub := ps.subscribe(seedFromSlice(initial))
	defer unsub()

	select {
	case got := <-ch:
		assert.Equal(t, initial, got)
	case <-time.After(time.Second):
		t.Fatal("did not receive initial snapshot")
	}
}

// Regression test: a publish racing with a fresh subscribe must overwrite
// the unread initial snapshot, not leave the consumer pinned to it.
func TestPubSub_PublishCoalescesUnreadInitialSnapshot(t *testing.T) {
	defer goleak.VerifyNone(t)

	ps := newPubSub(testlogger.New(t), "ns")
	initial := []string{"shard-init"}
	updated := []string{"shard-init", "shard-new"}

	ch, unsub := ps.subscribe(seedFromSlice(initial))
	defer unsub()

	ps.publish(updated)

	got := <-ch
	assert.Equal(t, updated, got, "consumer must observe the latest snapshot, not the stale initial")
}

// A second publish must replace an unread first publish.
func TestPubSub_PublishCoalescesUnreadPublish(t *testing.T) {
	defer goleak.VerifyNone(t)

	ps := newPubSub(testlogger.New(t), "ns")

	ch, unsub := ps.subscribe(nil)
	defer unsub()

	ps.publish([]string{"v1"})
	ps.publish([]string{"v1", "v2"})
	ps.publish([]string{"v3-only"})

	got := <-ch
	assert.Equal(t, []string{"v3-only"}, got, "only the most recent snapshot should remain in the buffer")
}

func TestPubSub_PublishAfterDrainDeliversLatest(t *testing.T) {
	defer goleak.VerifyNone(t)

	ps := newPubSub(testlogger.New(t), "ns")

	ch, unsub := ps.subscribe(nil)
	defer unsub()

	ps.publish([]string{"shard-1"})
	assert.Equal(t, []string{"shard-1"}, <-ch)

	ps.publish([]string{"shard-1", "shard-2"})
	assert.Equal(t, []string{"shard-1", "shard-2"}, <-ch)
}

func TestPubSub_NonBlockingPublishToSlowConsumer(t *testing.T) {
	defer goleak.VerifyNone(t)

	ps := newPubSub(testlogger.New(t), "ns")

	_, unsub := ps.subscribe(nil)
	defer unsub()

	done := make(chan struct{})
	go func() {
		for range 10 {
			ps.publish([]string{"shard-x"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked on slow consumer")
	}
}

func TestPubSub_MultipleSubscribersEachGetTheirSeed(t *testing.T) {
	defer goleak.VerifyNone(t)

	ps := newPubSub(testlogger.New(t), "ns")

	chA, unsubA := ps.subscribe(seedFromSlice([]string{"a-init"}))
	defer unsubA()
	chB, unsubB := ps.subscribe(seedFromSlice([]string{"b-init"}))
	defer unsubB()

	assert.Equal(t, []string{"a-init"}, <-chA)
	assert.Equal(t, []string{"b-init"}, <-chB)

	ps.publish([]string{"shared"})
	assert.Equal(t, []string{"shared"}, <-chA)
	assert.Equal(t, []string{"shared"}, <-chB)
}

// Subscribing with a nil seedFn registers without an initial value; the
// channel only receives subsequent publishes.
func TestPubSub_SubscribeWithoutSeed(t *testing.T) {
	defer goleak.VerifyNone(t)

	ps := newPubSub(testlogger.New(t), "ns")

	ch, unsub := ps.subscribe(nil)
	defer unsub()

	select {
	case <-ch:
		t.Fatal("expected no initial seed when seedFn is nil")
	case <-time.After(50 * time.Millisecond):
	}

	ps.publish([]string{"first"})
	assert.Equal(t, []string{"first"}, <-ch)
}

func TestPubSub_UnsubscribeIsIdempotent(t *testing.T) {
	defer goleak.VerifyNone(t)

	ps := newPubSub(testlogger.New(t), "ns")

	ch, unsub := ps.subscribe(nil)

	unsub()
	_, ok := <-ch
	assert.False(t, ok, "channel must be closed after unsubscribe")

	require.NotPanics(t, unsub)

	ps.mu.RLock()
	defer ps.mu.RUnlock()
	assert.Empty(t, ps.subscribers)
}

func TestPubSub_ConcurrentSubscribePublishDoesNotDeadlock(t *testing.T) {
	defer goleak.VerifyNone(t)

	ps := newPubSub(testlogger.New(t), "ns")

	const subscribers = 16
	var wg sync.WaitGroup
	wg.Add(subscribers)
	for i := 0; i < subscribers; i++ {
		go func() {
			defer wg.Done()
			ch, unsub := ps.subscribe(seedFromSlice([]string{"init"}))
			defer unsub()
			select {
			case <-ch:
			case <-time.After(time.Second):
				t.Errorf("subscriber did not receive initial snapshot")
			}
		}()
	}

	publishDone := make(chan struct{})
	go func() {
		defer close(publishDone)
		for i := 0; i < 100; i++ {
			ps.publish([]string{"snap"})
		}
	}()

	wg.Wait()
	<-publishDone
}
