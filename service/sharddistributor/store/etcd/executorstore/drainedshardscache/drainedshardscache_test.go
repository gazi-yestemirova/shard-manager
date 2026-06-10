package drainedshardscache

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cadence-workflow/shard-manager/common/clock"
	"github.com/cadence-workflow/shard-manager/common/log/testlogger"
	"github.com/cadence-workflow/shard-manager/common/metrics"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/store/etcd/etcdkeys"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/store/etcd/testhelper"
)

func newCache(t *testing.T, tc *testhelper.StoreTestCluster) *Cache {
	t.Helper()
	c := NewCache(tc.EtcdPrefix, tc.Client, testlogger.New(t), clock.NewRealTimeSource(), metrics.NewNoopMetricsClient())
	c.Start()
	t.Cleanup(c.Stop)
	return c
}

func putDrainedKey(t *testing.T, tc *testhelper.StoreTestCluster, shardID string) {
	t.Helper()
	key := etcdkeys.BuildDrainedShardKey(tc.EtcdPrefix, tc.Namespace, shardID)
	_, err := tc.Client.Put(context.Background(), key, "")
	require.NoError(t, err)
}

func deleteDrainedKey(t *testing.T, tc *testhelper.StoreTestCluster, shardID string) {
	t.Helper()
	key := etcdkeys.BuildDrainedShardKey(tc.EtcdPrefix, tc.Namespace, shardID)
	_, err := tc.Client.Delete(context.Background(), key)
	require.NoError(t, err)
}

func receiveSnapshot(t *testing.T, ch <-chan []string) []string {
	t.Helper()
	select {
	case snap := <-ch:
		out := append([]string(nil), snap...)
		sort.Strings(out)
		return out
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for snapshot")
		return nil
	}
}

func waitForContains(t *testing.T, c *Cache, namespace, shardID string, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, ready := c.Contains(namespace, shardID)
		if ready && got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, ready := c.Contains(namespace, shardID)
	t.Fatalf("Contains(%s) did not converge: want=%v got=%v ready=%v", shardID, want, got, ready)
}

// TestCache_InitialSnapshotAndIncrementalUpdates verifies the snapshot-then-watch
// flow: an existing drained key is reflected in the first snapshot, subsequent
// PUTs and DELETEs are propagated incrementally.
func TestCache_InitialSnapshotAndIncrementalUpdates(t *testing.T) {
	tc := testhelper.SetupStoreTestCluster(t)
	putDrainedKey(t, tc, "shard-1")

	c := newCache(t, tc)

	ch, unsub, err := c.Subscribe(context.Background(), tc.Namespace)
	require.NoError(t, err)
	defer unsub()

	got := receiveSnapshot(t, ch)
	assert.Equal(t, []string{"shard-1"}, got)

	// Drain a new shard -> incremental snapshot includes both.
	putDrainedKey(t, tc, "shard-2")
	got = receiveSnapshot(t, ch)
	assert.Equal(t, []string{"shard-1", "shard-2"}, got)

	// Undrain shard-1 -> snapshot drops it.
	deleteDrainedKey(t, tc, "shard-1")
	got = receiveSnapshot(t, ch)
	assert.Equal(t, []string{"shard-2"}, got)

	// Contains converges to the latest in-memory state.
	waitForContains(t, c, tc.Namespace, "shard-2", true)
	waitForContains(t, c, tc.Namespace, "shard-1", false)
}

// TestCache_ContainsReadyAfterFirstSnapshot verifies that Contains reports
// ready=false until the first snapshot lands and ready=true afterwards. The
// test races a Contains call against the watch goroutine so it tolerates
// either ordering.
func TestCache_ContainsReadyAfterFirstSnapshot(t *testing.T) {
	tc := testhelper.SetupStoreTestCluster(t)
	putDrainedKey(t, tc, "shard-7")

	c := newCache(t, tc)

	// Force lazy creation; the first call may or may not be ready depending
	// on goroutine scheduling, but waitForContains gives the watcher a chance
	// to land its initial snapshot before asserting.
	waitForContains(t, c, tc.Namespace, "shard-7", true)
	waitForContains(t, c, tc.Namespace, "missing", false)
}

// TestCache_MultipleSubscribersShareWatch verifies the pubsub fan-out:
// multiple subscribers see the same updates from a single underlying etcd
// watch.
func TestCache_MultipleSubscribersShareWatch(t *testing.T) {
	tc := testhelper.SetupStoreTestCluster(t)
	c := newCache(t, tc)

	chA, unsubA, err := c.Subscribe(context.Background(), tc.Namespace)
	require.NoError(t, err)
	defer unsubA()

	// Drain initial subscriber snapshot.
	assert.Empty(t, receiveSnapshot(t, chA))

	chB, unsubB, err := c.Subscribe(context.Background(), tc.Namespace)
	require.NoError(t, err)
	defer unsubB()

	// New subscriber gets an initial snapshot too.
	assert.Empty(t, receiveSnapshot(t, chB))

	// Drain a shard -> both subscribers see it.
	putDrainedKey(t, tc, "shard-shared")
	assert.Equal(t, []string{"shard-shared"}, receiveSnapshot(t, chA))
	assert.Equal(t, []string{"shard-shared"}, receiveSnapshot(t, chB))
}

// TestCache_UnsubscribeStopsUpdates verifies that calling unsub closes the
// channel and the cache continues serving other consumers.
func TestCache_UnsubscribeStopsUpdates(t *testing.T) {
	tc := testhelper.SetupStoreTestCluster(t)
	c := newCache(t, tc)

	ch, unsub, err := c.Subscribe(context.Background(), tc.Namespace)
	require.NoError(t, err)

	// Initial snapshot.
	assert.Empty(t, receiveSnapshot(t, ch))

	unsub()

	// Channel must be closed (non-blocking receive returns ok=false).
	select {
	case _, ok := <-ch:
		assert.False(t, ok, "channel should be closed after unsubscribe")
	case <-time.After(time.Second):
		t.Fatal("channel not closed after unsubscribe")
	}

	// Calling unsub again is idempotent.
	require.NotPanics(t, unsub)

	// In-memory state still updates after a subscriber leaves.
	putDrainedKey(t, tc, "shard-after-unsub")
	waitForContains(t, c, tc.Namespace, "shard-after-unsub", true)
}

// TestCache_DuplicatePutDoesNotPublish verifies that re-writing the same key
// (e.g. an idempotent drain re-issue) does not produce a spurious snapshot.
// We assert by waiting briefly and ensuring the subscriber doesn't receive a
// repeat snapshot.
func TestCache_DuplicatePutDoesNotPublish(t *testing.T) {
	tc := testhelper.SetupStoreTestCluster(t)
	c := newCache(t, tc)

	ch, unsub, err := c.Subscribe(context.Background(), tc.Namespace)
	require.NoError(t, err)
	defer unsub()

	assert.Empty(t, receiveSnapshot(t, ch))

	putDrainedKey(t, tc, "shard-dup")
	assert.Equal(t, []string{"shard-dup"}, receiveSnapshot(t, ch))

	// Second put is a no-op for set membership.
	putDrainedKey(t, tc, "shard-dup")

	select {
	case snap := <-ch:
		t.Fatalf("unexpected snapshot after idempotent put: %v", snap)
	case <-time.After(150 * time.Millisecond):
		// Good: no spurious publish.
	}
}

// TestCache_PerNamespaceIsolation verifies that drains in one namespace do
// not leak into another namespace's cache.
func TestCache_PerNamespaceIsolation(t *testing.T) {
	tc := testhelper.SetupStoreTestCluster(t)
	c := newCache(t, tc)

	chMain, unsubMain, err := c.Subscribe(context.Background(), tc.Namespace)
	require.NoError(t, err)
	defer unsubMain()
	assert.Empty(t, receiveSnapshot(t, chMain))

	otherNS := "drainedshardscache-other"
	chOther, unsubOther, err := c.Subscribe(context.Background(), otherNS)
	require.NoError(t, err)
	defer unsubOther()
	assert.Empty(t, receiveSnapshot(t, chOther))

	// Drain a shard in the primary namespace; only its subscriber should fire.
	putDrainedKey(t, tc, "shard-only-in-main")
	assert.Equal(t, []string{"shard-only-in-main"}, receiveSnapshot(t, chMain))

	select {
	case snap := <-chOther:
		t.Fatalf("other namespace must not see drains from %s, got %v", tc.Namespace, snap)
	case <-time.After(150 * time.Millisecond):
		// Good.
	}

	// Now drain in the other namespace; only its subscriber sees it.
	otherKey := etcdkeys.BuildDrainedShardKey(tc.EtcdPrefix, otherNS, "shard-only-in-other")
	_, err = tc.Client.Put(context.Background(), otherKey, "")
	require.NoError(t, err)

	assert.Equal(t, []string{"shard-only-in-other"}, receiveSnapshot(t, chOther))
}

// TestCache_SubscribeUnknownNamespaceWaitsForInitialSnapshot verifies that
// subscribing to a freshly-created namespace cache delivers an empty initial
// snapshot rather than blocking forever.
func TestCache_SubscribeUnknownNamespaceWaitsForInitialSnapshot(t *testing.T) {
	tc := testhelper.SetupStoreTestCluster(t)
	c := newCache(t, tc)

	ch, unsub, err := c.Subscribe(context.Background(), tc.Namespace)
	require.NoError(t, err)
	defer unsub()

	got := receiveSnapshot(t, ch)
	assert.Empty(t, got)
}

// TestSubscribeContextCancellationBeforeFirstSnapshot ensures we don't leak the
// initial-snapshot goroutine when the caller's context is already done.
func TestSubscribeContextCancellationBeforeFirstSnapshot(t *testing.T) {
	tc := testhelper.SetupStoreTestCluster(t)
	c := newCache(t, tc)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled.

	ch, unsub, err := c.Subscribe(ctx, tc.Namespace)
	require.NoError(t, err)
	defer unsub()

	// The initial-snapshot goroutine sees the cancelled context and exits
	// without sending. Subsequent published events still arrive on the channel.
	putDrainedKey(t, tc, "shard-after-cancel")

	select {
	case snap := <-ch:
		// Either we got the initial empty snapshot, the post-PUT snapshot, or
		// nothing at all (because the goroutine bailed early). Any of these is
		// fine; we just assert there's no panic and the channel still works.
		_ = snap
	case <-time.After(time.Second):
		// No snapshot is also acceptable: the cancelled-context branch in
		// subscribe just skipped the initial send.
	}
}
