// The MIT License (MIT)

// Copyright (c) 2017-2020 Uber Technologies Inc.

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cadence-workflow/shard-manager/common/clock"
	"github.com/cadence-workflow/shard-manager/common/log/testlogger"
	"github.com/cadence-workflow/shard-manager/common/types"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/config"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/store"
)

// Per-balancer placement logic (naive count, greedy smoothed-load, tiebreaks,
// draining/no-active handling) is covered in the loadbalancer package tests.
// These tests cover only handler-level concerns: storage orchestration, error
// wrapping, and the happy-path response shape.
func TestAssignEphemeralBatch(t *testing.T) {
	tests := []struct {
		name           string
		shardKeys      []string
		setupMocks     func(mockStore *store.MockStore)
		expectedOwners map[string]string // shardKey -> expected owner
		expectedError  bool
		expectedErrMsg string
	}{
		{
			name:      "HappyPath",
			shardKeys: []string{"NON-EXISTING-SHARD"},
			setupMocks: func(mockStore *store.MockStore) {
				mockStore.EXPECT().GetState(gomock.Any(), _testNamespaceEphemeral).Return(&store.NamespaceState{
					Executors: map[string]store.HeartbeatState{
						"owner1": {Status: types.ExecutorStatusACTIVE},
						"owner2": {Status: types.ExecutorStatusACTIVE},
					},
					ShardAssignments: map[string]store.AssignedState{
						"owner1": {AssignedShards: map[string]*types.ShardAssignment{
							"shard1": {Status: types.AssignmentStatusREADY},
							"shard2": {Status: types.AssignmentStatusREADY},
						}},
						"owner2": {AssignedShards: map[string]*types.ShardAssignment{
							"shard3": {Status: types.AssignmentStatusREADY},
						}},
					},
				}, nil)
				mockStore.EXPECT().AssignShards(gomock.Any(), _testNamespaceEphemeral, gomock.Any(), gomock.Any()).Return(nil)
				mockStore.EXPECT().GetExecutor(gomock.Any(), _testNamespaceEphemeral, "owner2").Return(&store.ShardOwner{
					ExecutorID: "owner2",
					Metadata:   map[string]string{"ip": "127.0.0.1", "port": "1234"},
				}, nil)
			},
			expectedOwners: map[string]string{"NON-EXISTING-SHARD": "owner2"},
		},
		{
			name:      "GetStateFailure",
			shardKeys: []string{"NON-EXISTING-SHARD"},
			setupMocks: func(mockStore *store.MockStore) {
				mockStore.EXPECT().GetState(gomock.Any(), _testNamespaceEphemeral).Return(nil, errors.New("get state failure"))
			},
			expectedError:  true,
			expectedErrMsg: "get state failure",
		},
		{
			// When two batches race and the first wins, the second gets
			// ErrVersionConflict from AssignShards. The handler returns this
			// unwrapped so callers can detect it with errors.Is and retry.
			name:      "VersionConflict",
			shardKeys: []string{"CONCURRENT-SHARD"},
			setupMocks: func(mockStore *store.MockStore) {
				mockStore.EXPECT().GetState(gomock.Any(), _testNamespaceEphemeral).Return(&store.NamespaceState{
					Executors:        map[string]store.HeartbeatState{"owner1": {Status: types.ExecutorStatusACTIVE}},
					ShardAssignments: map[string]store.AssignedState{"owner1": {AssignedShards: map[string]*types.ShardAssignment{}}},
				}, nil)
				mockStore.EXPECT().AssignShards(gomock.Any(), _testNamespaceEphemeral, gomock.Any(), gomock.Any()).Return(store.ErrVersionConflict)
			},
			expectedError:  true,
			expectedErrMsg: "version conflict",
		},
		{
			name:      "AssignShardsFailure",
			shardKeys: []string{"NON-EXISTING-SHARD"},
			setupMocks: func(mockStore *store.MockStore) {
				mockStore.EXPECT().GetState(gomock.Any(), _testNamespaceEphemeral).Return(&store.NamespaceState{
					Executors:        map[string]store.HeartbeatState{"owner1": {Status: types.ExecutorStatusACTIVE}},
					ShardAssignments: map[string]store.AssignedState{"owner1": {AssignedShards: map[string]*types.ShardAssignment{}}},
				}, nil)
				mockStore.EXPECT().AssignShards(gomock.Any(), _testNamespaceEphemeral, gomock.Any(), gomock.Any()).Return(errors.New("assign shards failure"))
			},
			expectedError:  true,
			expectedErrMsg: "assign shards failure",
		},
		{
			name:      "NoActiveExecutors",
			shardKeys: []string{"NON-EXISTING-SHARD"},
			setupMocks: func(mockStore *store.MockStore) {
				mockStore.EXPECT().GetState(gomock.Any(), _testNamespaceEphemeral).Return(&store.NamespaceState{
					Executors: map[string]store.HeartbeatState{"owner1": {Status: types.ExecutorStatusDRAINING}},
				}, nil)
			},
			expectedError:  true,
			expectedErrMsg: "plan initial placement: no active executors available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockStorage := store.NewMockStore(ctrl)

			h := &handlerImpl{
				logger:     testlogger.New(t),
				storage:    mockStorage,
				cfg:        newTestShardDistributorConfig(config.LoadBalancingModeNAIVE),
				timeSource: clock.NewRealTimeSource(),
			}

			tt.setupMocks(mockStorage)

			results, _, err := h.assignEphemeralBatch(context.Background(), _testNamespaceEphemeral, tt.shardKeys)
			if tt.expectedError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.expectedErrMsg)
				require.Nil(t, results)
				return
			}
			require.NoError(t, err)
			require.Len(t, results, len(tt.expectedOwners))
			for shardKey, expectedOwner := range tt.expectedOwners {
				require.Equal(t, expectedOwner, results[shardKey].Owner)
				require.Equal(t, _testNamespaceEphemeral, results[shardKey].Namespace)
				require.Equal(t, map[string]string{"ip": "127.0.0.1", "port": "1234"}, results[shardKey].Metadata)
			}
		})
	}
}

// Drained shards in a batch must be filtered out from balancer input and
// surfaced as per-shard ShardDrainedError in the second return value.
// Non-drained shards are still assigned in the same batch.
func TestAssignEphemeralBatch_DrainedShardsFilteredAndReportedPerShard(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := store.NewMockStore(ctrl)
	h := &handlerImpl{
		logger:     testlogger.New(t),
		storage:    mockStorage,
		cfg:        newTestShardDistributorConfig(config.LoadBalancingModeNAIVE),
		timeSource: clock.NewRealTimeSource(),
	}

	mockStorage.EXPECT().GetState(gomock.Any(), _testNamespaceEphemeral).Return(&store.NamespaceState{
		Executors: map[string]store.HeartbeatState{
			"owner1": {Status: types.ExecutorStatusACTIVE},
		},
		ShardAssignments: map[string]store.AssignedState{
			"owner1": {AssignedShards: map[string]*types.ShardAssignment{}},
		},
		DrainedShards: map[string]struct{}{"DRAINED-A": {}, "DRAINED-B": {}},
	}, nil)
	mockStorage.EXPECT().AssignShards(gomock.Any(), _testNamespaceEphemeral, gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, request store.AssignShardsRequest, _ store.GuardFunc) error {
			assigned := request.NewState.ShardAssignments["owner1"].AssignedShards
			require.Contains(t, assigned, "OK-1")
			require.NotContains(t, assigned, "DRAINED-A")
			require.NotContains(t, assigned, "DRAINED-B")
			return nil
		},
	)
	mockStorage.EXPECT().GetExecutor(gomock.Any(), _testNamespaceEphemeral, "owner1").Return(&store.ShardOwner{
		ExecutorID: "owner1",
		Metadata:   map[string]string{"ip": "127.0.0.1"},
	}, nil)

	results, perShardErrors, err := h.assignEphemeralBatch(context.Background(), _testNamespaceEphemeral, []string{"OK-1", "DRAINED-A", "DRAINED-B"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "owner1", results["OK-1"].Owner)
	require.Len(t, perShardErrors, 2)
	for _, key := range []string{"DRAINED-A", "DRAINED-B"} {
		var drainedErr *types.ShardDrainedError
		require.ErrorAsf(t, perShardErrors[key], &drainedErr, "expected ShardDrainedError for %s", key)
		require.Equal(t, key, drainedErr.ShardKey)
		require.Equal(t, _testNamespaceEphemeral, drainedErr.Namespace)
	}
}

// All shards in a batch are drained: nothing to assign, so the assigner must
// not call AssignShards/GetExecutor on the storage layer.
func TestAssignEphemeralBatch_AllDrainedSkipsAssignment(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := store.NewMockStore(ctrl)
	h := &handlerImpl{
		logger:     testlogger.New(t),
		storage:    mockStorage,
		cfg:        newTestShardDistributorConfig(config.LoadBalancingModeNAIVE),
		timeSource: clock.NewRealTimeSource(),
	}

	mockStorage.EXPECT().GetState(gomock.Any(), _testNamespaceEphemeral).Return(&store.NamespaceState{
		Executors:        map[string]store.HeartbeatState{"owner1": {Status: types.ExecutorStatusACTIVE}},
		ShardAssignments: map[string]store.AssignedState{"owner1": {AssignedShards: map[string]*types.ShardAssignment{}}},
		DrainedShards:    map[string]struct{}{"D-1": {}, "D-2": {}},
	}, nil)

	results, perShardErrors, err := h.assignEphemeralBatch(context.Background(), _testNamespaceEphemeral, []string{"D-1", "D-2"})
	require.NoError(t, err)
	require.Empty(t, results)
	require.Len(t, perShardErrors, 2)
}

// An unsupported load balancing mode bubbles up from the loadbalancer planner as an
// InternalServiceError; the handler wraps it rather than panicking.
func TestAssignEphemeralBatch_InvalidLoadBalancingMode(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := store.NewMockStore(ctrl)
	h := &handlerImpl{
		logger:     testlogger.New(t),
		storage:    mockStorage,
		cfg:        newTestShardDistributorConfig("not-a-valid-mode"),
		timeSource: clock.NewRealTimeSource(),
	}

	mockStorage.EXPECT().GetState(gomock.Any(), _testNamespaceEphemeral).Return(&store.NamespaceState{
		Executors:        map[string]store.HeartbeatState{"owner1": {Status: types.ExecutorStatusACTIVE}},
		ShardAssignments: map[string]store.AssignedState{"owner1": {AssignedShards: map[string]*types.ShardAssignment{}}},
	}, nil)

	results, _, err := h.assignEphemeralBatch(context.Background(), _testNamespaceEphemeral, []string{"new-shard-1"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported load balancing mode")
	require.Nil(t, results)
}
