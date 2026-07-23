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

// Package ephemeralassigner owns ephemeral shard assignment for the shard
// distributor: it batches concurrent GetShardOwner cache misses for a namespace,
// assigns them in a single storage write, and retries on version conflicts. It is
// kept separate from the RPC handler so the handler stays limited to transport and
// type conversion.
package ephemeralassigner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cadence-workflow/shard-manager/common/backoff"
	"github.com/cadence-workflow/shard-manager/common/clock"
	"github.com/cadence-workflow/shard-manager/common/log"
	"github.com/cadence-workflow/shard-manager/common/types"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/config"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/loadbalancer"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/loadbalancer/plan"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/store"
)

const (
	// DefaultBatchInterval is the time window over which GetShardOwner calls for
	// ephemeral namespaces are collected before being processed as a single batch.
	DefaultBatchInterval = 100 * time.Millisecond

	// versionConflictRetryInitialInterval is the starting backoff for retries
	// triggered when a concurrent shard assignment causes a version conflict.
	versionConflictRetryInitialInterval = 50 * time.Millisecond
	// versionConflictRetryMaxInterval caps the per-attempt sleep.
	versionConflictRetryMaxInterval = 1 * time.Second
	// versionConflictRetryMaxAttempts is the maximum number of retry attempts
	// before the error is surfaced to the caller.
	versionConflictRetryMaxAttempts = 3
)

// Assigner batches and assigns ephemeral shards. Construct it with New, call Start
// before serving requests, and Stop on shutdown.
type Assigner struct {
	logger     log.Logger
	storage    store.Store
	cfg        *config.Config
	timeSource clock.TimeSource

	batcher *shardBatcher
}

// New constructs an Assigner that collects requests over batchInterval.
func New(
	logger log.Logger,
	timeSource clock.TimeSource,
	cfg *config.Config,
	storage store.Store,
	batchInterval time.Duration,
) *Assigner {
	a := &Assigner{
		logger:     logger,
		storage:    storage,
		cfg:        cfg,
		timeSource: timeSource,
	}
	a.batcher = newShardBatcher(timeSource, batchInterval, a.assignBatch)
	return a
}

// Start launches the background batching loop.
func (a *Assigner) Start() { a.batcher.Start() }

// Stop drains in-flight requests and shuts the batching loop down.
func (a *Assigner) Stop() { a.batcher.Stop() }

// GetOrAssign returns the owner of an ephemeral shard, assigning it if it does not
// yet exist. It submits the request to the batcher and, on a version conflict
// (concurrent assignment by another goroutine), retries with exponential backoff.
// Each retry re-reads storage first: if the concurrent writer already committed the
// assignment we return it immediately without re-submitting to the batcher.
func (a *Assigner) GetOrAssign(ctx context.Context, request *types.GetShardOwnerRequest) (*types.GetShardOwnerResponse, error) {
	retryPolicy := backoff.NewExponentialRetryPolicy(versionConflictRetryInitialInterval)
	retryPolicy.SetMaximumInterval(versionConflictRetryMaxInterval)
	retryPolicy.SetMaximumAttempts(versionConflictRetryMaxAttempts)

	throttleRetry := backoff.NewThrottleRetry(
		backoff.WithRetryPolicy(retryPolicy),
		backoff.WithRetryableError(func(err error) bool {
			return errors.Is(err, store.ErrVersionConflict)
		}),
	)

	var resp *types.GetShardOwnerResponse
	isRetry := false
	err := throttleRetry.Do(ctx, func(ctx context.Context) error {
		if isRetry {
			// A concurrent batch won the race. Re-read storage first: if the
			// winner already committed our shard's assignment we can return
			// immediately without re-submitting to the batcher.
			owner, err := a.storage.GetShardOwner(ctx, request.Namespace, request.ShardKey)
			if err != nil && !errors.Is(err, store.ErrShardNotFound) {
				return &types.InternalServiceError{Message: fmt.Sprintf("failed to get shard owner: %v", err)}
			}
			if err == nil {
				resp = &types.GetShardOwnerResponse{
					Owner:     owner.ExecutorID,
					Metadata:  owner.Metadata,
					Namespace: request.Namespace,
				}
				return nil
			}
		}
		isRetry = true

		// Submit to the batcher to assign the shard.
		var err error
		resp, err = a.batcher.Submit(ctx, request)
		return err
	})

	if err != nil {
		return nil, &types.InternalServiceError{Message: fmt.Sprintf("failed to assign ephemeral shard: %v", err)}
	}
	return resp, nil
}

// assignBatch is the batch function wired into the shardBatcher. It processes a
// whole batch of shard keys for a single ephemeral namespace using at most two
// storage operations:
//  1. GetState — read current namespace state once for the whole batch.
//  2. AssignShards — write all new assignments atomically in one operation
//     (skipped entirely when every requested shard is already assigned).
//
// Duplicate-ownership guard:
// already-owned shards reuse their current owner, and repeated keys collapse to a
// single placement, so a shard is never assigned to more than one executor when
// many callers race a (re)created key within one batch window.
//
// After the write, GetExecutor is called once per unique executor referenced by
// the resulting owners (not per shard) to fetch metadata for the response, since
// metadata is stored separately in the shard cache and is not returned by
// GetState.
func (a *Assigner) assignBatch(ctx context.Context, namespace string, shardKeys []string) (map[string]*types.GetShardOwnerResponse, error) {
	state, err := a.storage.GetState(ctx, namespace)
	if err != nil {
		return nil, &types.InternalServiceError{Message: fmt.Sprintf("get namespace state: %v", err)}
	}

	executorByShard, toPlace := resolveOwners(state, shardKeys)

	if len(toPlace) > 0 {
		placements, err := loadbalancer.PlanInitialPlacement(a.cfg, namespace, state, toPlace)
		if err != nil {
			return nil, &types.InternalServiceError{Message: fmt.Sprintf("plan initial placement: %v", err)}
		}

		mergePlacements(state, placements, a.timeSource.Now().UTC())

		if err := a.storage.AssignShards(ctx, namespace, store.AssignShardsRequest{NewState: state}, store.NopGuard()); err != nil {
			if errors.Is(err, store.ErrVersionConflict) {
				// Keep the sentinel wrapped so callers can detect it and retry.
				return nil, fmt.Errorf("assign ephemeral shards: %w", err)
			}
			return nil, &types.InternalServiceError{Message: fmt.Sprintf("assign ephemeral shards: %v", err)}
		}

		for _, placement := range placements {
			executorByShard[placement.ShardID] = placement.ExecutorID
		}
	}

	executorOwners, err := a.fetchExecutorMetadata(ctx, namespace, executorByShard)
	if err != nil {
		return nil, err
	}

	return buildResults(namespace, shardKeys, executorByShard, executorOwners), nil
}

// resolveOwners splits requested shards into those already assigned (mapped to
// their current owner) and those still needing placement. Already-owned shards are
// never re-placed; the returned toPlace slice is deduplicated by the load balancer.
func resolveOwners(state *store.NamespaceState, shardKeys []string) (executorByShard map[string]string, toPlace []string) {
	owners := state.ActiveShardOwners()
	executorByShard = make(map[string]string, len(shardKeys))
	for _, shardKey := range shardKeys {
		if executorID, ok := owners[shardKey]; ok {
			executorByShard[shardKey] = executorID
			continue
		}
		toPlace = append(toPlace, shardKey)
	}
	return executorByShard, toPlace
}

// mergePlacements folds the planned shard→executor placements back into state.
// The AssignedShards maps are copied to avoid mutating the object returned by
// GetState.
func mergePlacements(state *store.NamespaceState, placements []plan.Placement, now time.Time) {
	if state.ShardAssignments == nil {
		state.ShardAssignments = make(map[string]store.AssignedState)
	}
	for executorID, shardsForExecutor := range placementsByExecutor(placements) {
		existing := state.ShardAssignments[executorID]
		newShards := make(map[string]*types.ShardAssignment, len(existing.AssignedShards)+len(shardsForExecutor))
		for k, v := range existing.AssignedShards {
			newShards[k] = v
		}
		for _, shardKey := range shardsForExecutor {
			newShards[shardKey] = &types.ShardAssignment{Status: types.AssignmentStatusREADY}
		}
		existing.AssignedShards = newShards
		existing.LastUpdated = now
		state.ShardAssignments[executorID] = existing
	}
}

// fetchExecutorMetadata calls GetExecutor once per unique executor, since metadata
// is stored separately from HeartbeatState and is not returned by GetState.
func (a *Assigner) fetchExecutorMetadata(ctx context.Context, namespace string, executorByShard map[string]string) (map[string]*store.ShardOwner, error) {
	executorOwners := make(map[string]*store.ShardOwner, len(executorByShard))
	for _, executorID := range executorByShard {
		if _, already := executorOwners[executorID]; already {
			continue
		}
		owner, err := a.storage.GetExecutor(ctx, namespace, executorID)
		if err != nil {
			return nil, &types.InternalServiceError{Message: fmt.Sprintf("get executor %q: %v", executorID, err)}
		}
		executorOwners[executorID] = owner
	}
	return executorOwners, nil
}

// buildResults builds the shardKey -> GetShardOwnerResponse map from the resolved
// owners and their metadata.
func buildResults(namespace string, shardKeys []string, executorByShard map[string]string, executorOwners map[string]*store.ShardOwner) map[string]*types.GetShardOwnerResponse {
	results := make(map[string]*types.GetShardOwnerResponse, len(shardKeys))
	for _, shardKey := range shardKeys {
		executorID := executorByShard[shardKey]
		owner := executorOwners[executorID]
		results[shardKey] = &types.GetShardOwnerResponse{
			Owner:     owner.ExecutorID,
			Namespace: namespace,
			Metadata:  owner.Metadata,
		}
	}
	return results
}

// placementsByExecutor turns planned placements into map[executorID][]shardKey.
func placementsByExecutor(placements []plan.Placement) map[string][]string {
	out := make(map[string][]string)
	for _, placement := range placements {
		out[placement.ExecutorID] = append(out[placement.ExecutorID], placement.ShardID)
	}
	return out
}
