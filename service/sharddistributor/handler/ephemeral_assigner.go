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
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/cadence-workflow/shard-manager/common/types"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/loadbalancer"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/loadbalancer/plan"
	"github.com/cadence-workflow/shard-manager/service/sharddistributor/store"
)

// assignEphemeralBatch is the ephemeralAssignmentBatchFn wired into the shardBatcher.
// It processes a whole batch of shard keys for a single ephemeral namespace using
// at most two storage operations:
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
func (h *handlerImpl) assignEphemeralBatch(ctx context.Context, namespace string, shardKeys []string) (map[string]*types.GetShardOwnerResponse, error) {
	state, err := h.storage.GetState(ctx, namespace)
	if err != nil {
		return nil, &types.InternalServiceError{Message: fmt.Sprintf("get namespace state: %v", err)}
	}

	executorByShard, toPlace := resolveOwners(state, shardKeys)

	if len(toPlace) > 0 {
		placements, err := loadbalancer.PlanInitialPlacement(h.cfg, namespace, state, slices.Collect(maps.Keys(toPlace)))
		if err != nil {
			return nil, &types.InternalServiceError{Message: fmt.Sprintf("plan initial placement: %v", err)}
		}

		mergePlacements(state, placements, h.timeSource.Now().UTC())

		if err := h.storage.AssignShards(ctx, namespace, store.AssignShardsRequest{NewState: state}, store.NopGuard()); err != nil {
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

	executorOwners, err := h.fetchExecutorMetadata(ctx, namespace, executorByShard)
	if err != nil {
		return nil, err
	}

	return buildResults(namespace, shardKeys, executorByShard, executorOwners), nil
}

// resolveOwners splits requested shards into those already assigned (mapped to
// their current owner) and those still needing placement.
// toPlace is a set, so repeated keys collapse, and already-owned shards are never re-placed.
func resolveOwners(state *store.NamespaceState, shardKeys []string) (executorByShard map[string]string, toPlace map[string]struct{}) {
	owners := state.ActiveShardOwners()
	executorByShard = make(map[string]string, len(shardKeys))
	toPlace = make(map[string]struct{}, len(shardKeys))
	for _, shardKey := range shardKeys {
		if executorID, ok := owners[shardKey]; ok {
			executorByShard[shardKey] = executorID
			continue
		}
		toPlace[shardKey] = struct{}{}
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
func (h *handlerImpl) fetchExecutorMetadata(ctx context.Context, namespace string, executorByShard map[string]string) (map[string]*store.ShardOwner, error) {
	executorOwners := make(map[string]*store.ShardOwner, len(executorByShard))
	for _, executorID := range executorByShard {
		if _, already := executorOwners[executorID]; already {
			continue
		}
		owner, err := h.storage.GetExecutor(ctx, namespace, executorID)
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
