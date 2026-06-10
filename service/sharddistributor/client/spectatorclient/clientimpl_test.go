package spectatorclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uber-go/tally"
	"go.uber.org/goleak"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/cadence-workflow/shard-manager/client/sharddistributor"
	"github.com/cadence-workflow/shard-manager/common/clock"
	"github.com/cadence-workflow/shard-manager/common/types"
	csync "github.com/cadence-workflow/shard-manager/service/sharddistributor/client/spectatorclient/sync"
)

func TestWatchLoopBasicFlow(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctrl := gomock.NewController(t)
	mockClient := sharddistributor.NewMockClient(ctrl)
	mockStream := sharddistributor.NewMockWatchNamespaceStateClient(ctrl)

	// Create a context to control when the mock stream should unblock
	streamCtx, cancelStream := context.WithCancel(context.Background())

	spectator := &spectatorImpl{
		namespace:        "test-ns",
		client:           mockClient,
		logger:           zap.NewNop(),
		scope:            tally.NoopScope,
		timeSource:       clock.NewRealTimeSource(),
		firstStateSignal: csync.NewResettableSignal(),
		enabled:          func() bool { return true },
	}

	// Expect stream creation
	mockClient.EXPECT().
		WatchNamespaceState(gomock.Any(), &types.WatchNamespaceStateRequest{Namespace: "test-ns"}).
		Return(mockStream, nil)

	// First Recv returns state
	mockStream.EXPECT().Recv().Return(&types.WatchNamespaceStateResponse{
		Executors: []*types.ExecutorShardAssignment{
			{
				ExecutorID: "executor-1",
				Metadata: map[string]string{
					"grpc_address": "127.0.0.1:7953",
				},
				AssignedShards: []*types.Shard{
					{ShardKey: "shard-1"},
					{ShardKey: "shard-2"},
				},
			},
		},
	}, nil)

	// Second Recv blocks until shutdown
	mockStream.EXPECT().Recv().DoAndReturn(func(...interface{}) (*types.WatchNamespaceStateResponse, error) {
		// Wait for context to be done
		<-streamCtx.Done()
		return nil, streamCtx.Err()
	})

	ctx := context.Background()
	err := spectator.Start(ctx)
	require.NoError(t, err)
	defer func() {
		cancelStream()
		spectator.Stop()
	}()

	// Wait for first state
	require.NoError(t, spectator.firstStateSignal.Wait(context.Background()))

	// Query shard owner
	owner, err := spectator.GetShardOwner(context.Background(), "shard-1")
	assert.NoError(t, err)
	assert.Equal(t, "executor-1", owner.ExecutorID)
	assert.Equal(t, "127.0.0.1:7953", owner.Metadata["grpc_address"])

	owner, err = spectator.GetShardOwner(context.Background(), "shard-2")
	assert.NoError(t, err)
	assert.Equal(t, "executor-1", owner.ExecutorID)
}

func TestGetShardOwner_CacheMiss_FallbackToRPC(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctrl := gomock.NewController(t)
	mockClient := sharddistributor.NewMockClient(ctrl)
	mockStream := sharddistributor.NewMockWatchNamespaceStateClient(ctrl)

	// Create a context to control when the mock stream should unblock
	streamCtx, cancelStream := context.WithCancel(context.Background())

	spectator := &spectatorImpl{
		namespace:        "test-ns",
		client:           mockClient,
		logger:           zap.NewNop(),
		scope:            tally.NoopScope,
		timeSource:       clock.NewRealTimeSource(),
		firstStateSignal: csync.NewResettableSignal(),
		enabled:          func() bool { return true },
	}

	// Setup stream
	mockClient.EXPECT().
		WatchNamespaceState(gomock.Any(), gomock.Any()).
		Return(mockStream, nil)

	// First Recv returns state
	mockStream.EXPECT().Recv().Return(&types.WatchNamespaceStateResponse{
		Executors: []*types.ExecutorShardAssignment{
			{
				ExecutorID: "executor-1",
				Metadata: map[string]string{
					"grpc_address": "127.0.0.1:7953",
				},
				AssignedShards: []*types.Shard{{ShardKey: "shard-1"}},
			},
		},
	}, nil)

	// Second Recv blocks until shutdown
	mockStream.EXPECT().Recv().AnyTimes().DoAndReturn(func(...interface{}) (*types.WatchNamespaceStateResponse, error) {
		// Wait for context to be done
		<-streamCtx.Done()
		return nil, streamCtx.Err()
	})

	// Expect RPC fallback for unknown shard
	mockClient.EXPECT().
		GetShardOwner(gomock.Any(), &types.GetShardOwnerRequest{
			Namespace: "test-ns",
			ShardKey:  "unknown-shard",
		}).
		Return(&types.GetShardOwnerResponse{
			Owner: "executor-2",
			Metadata: map[string]string{
				"grpc_address": "127.0.0.1:7954",
			},
		}, nil)

	spectator.Start(context.Background())
	defer func() {
		cancelStream()
		spectator.Stop()
	}()

	require.NoError(t, spectator.firstStateSignal.Wait(context.Background()))

	// Cache hit
	owner, err := spectator.GetShardOwner(context.Background(), "shard-1")
	assert.NoError(t, err)
	assert.Equal(t, "executor-1", owner.ExecutorID)

	// Cache miss - should trigger RPC
	owner, err = spectator.GetShardOwner(context.Background(), "unknown-shard")
	assert.NoError(t, err)
	assert.Equal(t, "executor-2", owner.ExecutorID)
	assert.Equal(t, "127.0.0.1:7954", owner.Metadata["grpc_address"])
}

func TestStreamReconnection(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctrl := gomock.NewController(t)
	mockClient := sharddistributor.NewMockClient(ctrl)
	mockStream1 := sharddistributor.NewMockWatchNamespaceStateClient(ctrl)
	mockStream2 := sharddistributor.NewMockWatchNamespaceStateClient(ctrl)
	mockTimeSource := clock.NewMockedTimeSource()
	testScope := tally.NewTestScope("", nil)

	// Create a context to control when the mock stream should unblock
	streamCtx, cancelStream := context.WithCancel(context.Background())

	spectator := &spectatorImpl{
		namespace:        "test-ns",
		client:           mockClient,
		logger:           zap.NewNop(),
		scope:            testScope,
		timeSource:       mockTimeSource,
		firstStateSignal: csync.NewResettableSignal(),
		enabled:          func() bool { return true },
	}

	// First stream fails immediately
	mockClient.EXPECT().
		WatchNamespaceState(gomock.Any(), gomock.Any()).
		Return(mockStream1, nil)

	mockStream1.EXPECT().Recv().Return(nil, errors.New("network error"))

	// Second stream succeeds
	mockClient.EXPECT().
		WatchNamespaceState(gomock.Any(), gomock.Any()).
		Return(mockStream2, nil)

	// First Recv returns state
	mockStream2.EXPECT().Recv().Return(&types.WatchNamespaceStateResponse{
		Executors: []*types.ExecutorShardAssignment{{ExecutorID: "executor-1"}},
	}, nil)

	// Second Recv blocks until shutdown
	mockStream2.EXPECT().Recv().AnyTimes().DoAndReturn(func(...interface{}) (*types.WatchNamespaceStateResponse, error) {
		// Wait for context to be done
		<-streamCtx.Done()
		return nil, errors.New("shutdown")
	})

	spectator.Start(context.Background())
	defer func() {
		cancelStream()
		spectator.Stop()
	}()

	// Wait for the goroutine to be blocked in Sleep, then advance time
	// BlockUntil(2) because connectState creates an AfterFunc timer (waiter #1)
	// and the retry SleepWithContext creates another (waiter #2)
	mockTimeSource.BlockUntil(2)
	mockTimeSource.Advance(2 * time.Second)

	require.NoError(t, spectator.firstStateSignal.Wait(context.Background()))

	errorReconnects := testScope.Snapshot().Counters()["shard_distributor_spectator_stream_reconnects+reason=error"]
	require.NotNil(t, errorReconnects)
	assert.Equal(t, int64(1), errorReconnects.Value())
}

func TestGetShardOwner_TimeoutBeforeFirstState(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctrl := gomock.NewController(t)
	mockClient := sharddistributor.NewMockClient(ctrl)

	spectator := &spectatorImpl{
		namespace:        "test-ns",
		client:           mockClient,
		logger:           zap.NewNop(),
		scope:            tally.NoopScope,
		timeSource:       clock.NewRealTimeSource(),
		firstStateSignal: csync.NewResettableSignal(),
		enabled:          func() bool { return true },
	}

	// Create a context with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	// Try to get shard owner before first state is received
	// Should timeout and return an error
	_, err := spectator.GetShardOwner(ctx, "shard-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "wait for first state")
}

// TestGetShardOwner_DrainedShortCircuits exercises the local drained-set cache
// added alongside the WatchNamespaceState stream. A drained shard must return
// types.ShardDrainedError without ever invoking the underlying client, while a
// shard that gets undrained on the next push must fall through to the cached
// owner without triggering an RPC.
func TestGetShardOwner_DrainedShortCircuits(t *testing.T) {
	ctrl := gomock.NewController(t)
	// Strict: any GetShardOwner call would fail the test (see assertions below).
	mockClient := sharddistributor.NewMockClient(ctrl)

	spectator := &spectatorImpl{
		namespace:        "test-ns",
		client:           mockClient,
		logger:           zap.NewNop(),
		scope:            tally.NoopScope,
		timeSource:       clock.NewRealTimeSource(),
		firstStateSignal: csync.NewResettableSignal(),
		enabled:          func() bool { return true },
	}

	// First push: shard-1 owned by executor-1, shard-7 currently drained.
	spectator.handleResponse(&types.WatchNamespaceStateResponse{
		Executors: []*types.ExecutorShardAssignment{
			{
				ExecutorID: "executor-1",
				AssignedShards: []*types.Shard{
					{ShardKey: "shard-1"},
					{ShardKey: "shard-7"},
				},
			},
		},
		DrainedShardKeys: []string{"shard-7"},
	})

	owner, err := spectator.GetShardOwner(context.Background(), "shard-1")
	require.NoError(t, err)
	require.Equal(t, "executor-1", owner.ExecutorID)

	// Drained shard short-circuits: no RPC, returns ShardDrainedError.
	_, err = spectator.GetShardOwner(context.Background(), "shard-7")
	require.Error(t, err)
	var drainedErr *types.ShardDrainedError
	require.ErrorAs(t, err, &drainedErr)
	require.Equal(t, "shard-7", drainedErr.ShardKey)
	require.Equal(t, "test-ns", drainedErr.Namespace)

	// Next push: shard-7 undrained, still owned by executor-1.
	spectator.handleResponse(&types.WatchNamespaceStateResponse{
		Executors: []*types.ExecutorShardAssignment{
			{
				ExecutorID: "executor-1",
				AssignedShards: []*types.Shard{
					{ShardKey: "shard-1"},
					{ShardKey: "shard-7"},
				},
			},
		},
		DrainedShardKeys: nil,
	})

	owner, err = spectator.GetShardOwner(context.Background(), "shard-7")
	require.NoError(t, err)
	require.Equal(t, "executor-1", owner.ExecutorID)
}

func TestWatchLoopDisabled(t *testing.T) {
	defer goleak.VerifyNone(t)

	stateSignal := csync.NewResettableSignal()
	timeSource := clock.NewMockedTimeSource()

	spectator := &spectatorImpl{
		firstStateSignal: stateSignal,
		timeSource:       timeSource,
		logger:           zap.NewNop(),
		enabled:          func() bool { return false },
	}

	err := spectator.Start(context.Background())
	assert.NoError(t, err)

	// Disabled state enters a sleep loop, verify it sleeps periodically
	timeSource.BlockUntil(1)
	timeSource.Advance(1200 * time.Millisecond)

	timeSource.BlockUntil(1)
	timeSource.Advance(1200 * time.Millisecond)

	// Stop exits cleanly and calls Done() on the signal
	spectator.Stop()

	// After Stop(), Done() has been called so Wait returns nil
	err = stateSignal.Wait(context.Background())
	assert.NoError(t, err)
}
