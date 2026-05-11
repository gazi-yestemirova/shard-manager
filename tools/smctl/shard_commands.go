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

package smctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	cliv3 "github.com/urfave/cli/v3"

	"github.com/cadence-workflow/shard-manager/common/types"
)

// shardCommand returns the "shard" command group. All of its subcommands
// operate on the namespace identified by the persistent --namespace/-n flag
// declared on the root smctl command.
//
// Examples:
//
//	smctl -n <ns> shard drain --shard 1 --shard 2
//	smctl -n <ns> shard undrain --shard 1
//	smctl -n <ns> shard list-drained
func shardCommand(cf ClientFactory) *cliv3.Command {
	return &cliv3.Command{
		Name:        "shard",
		Aliases:     []string{"sh"},
		Usage:       "Inspect and manage individual shards within a namespace",
		Description: "Use --namespace/-n on the root command to identify the target namespace.",
		Commands: []*cliv3.Command{
			drainShardsCommand(cf),
			undrainShardsCommand(cf),
			listDrainedShardsCommand(cf),
		},
	}
}

func drainShardsCommand(cf ClientFactory) *cliv3.Command {
	return &cliv3.Command{
		Name:        "drain",
		Usage:       "Mark one or more shards as drained for the namespace",
		Description: "Drained shards are removed from their current executor on the next rebalance and are not eligible for assignment until UndrainShards is called.",
		Flags: []cliv3.Flag{
			&cliv3.StringSliceFlag{
				Name:     FlagShardKey,
				Aliases:  []string{"s"},
				Usage:    "shard key to drain (repeat the flag to drain multiple shards)",
				Required: true,
			},
		},
		Action: func(ctx context.Context, cmd *cliv3.Command) error {
			return runDrainShards(ctx, cmd, resolveWriter(cmd), cf)
		},
	}
}

func undrainShardsCommand(cf ClientFactory) *cliv3.Command {
	return &cliv3.Command{
		Name:        "undrain",
		Usage:       "Remove one or more shards from the drained list",
		Description: "Undrained shards become eligible for assignment again on the next rebalance.",
		Flags: []cliv3.Flag{
			&cliv3.StringSliceFlag{
				Name:     FlagShardKey,
				Aliases:  []string{"s"},
				Usage:    "shard key to undrain (repeat the flag for multiple shards)",
				Required: true,
			},
		},
		Action: func(ctx context.Context, cmd *cliv3.Command) error {
			return runUndrainShards(ctx, cmd, resolveWriter(cmd), cf)
		},
	}
}

func listDrainedShardsCommand(cf ClientFactory) *cliv3.Command {
	return &cliv3.Command{
		Name:        "list-drained",
		Aliases:     []string{"ld"},
		Usage:       "List all drained shards for the namespace",
		Description: "Calls GetDrainedShards on shard-manager and prints the response as indented JSON.",
		Action: func(ctx context.Context, cmd *cliv3.Command) error {
			return runListDrainedShards(ctx, cmd, resolveWriter(cmd), cf)
		},
	}
}

func runDrainShards(ctx context.Context, cmd *cliv3.Command, out io.Writer, cf ClientFactory) error {
	namespace, shardKeys, err := requireNamespaceAndShards(cmd)
	if err != nil {
		return err
	}

	client, err := cf.ShardManagerClient(cmd)
	if err != nil {
		return err
	}

	callCtx, cancel := context.WithTimeout(ctx, cmd.Duration(FlagContextTimeout))
	defer cancel()

	resp, err := client.DrainShards(callCtx, &types.DrainShardsRequest{
		Namespace: namespace,
		ShardKeys: shardKeys,
	})
	if err != nil {
		return fmt.Errorf("DrainShards: %w", err)
	}
	return encodeJSON(out, resp)
}

func runUndrainShards(ctx context.Context, cmd *cliv3.Command, out io.Writer, cf ClientFactory) error {
	namespace, shardKeys, err := requireNamespaceAndShards(cmd)
	if err != nil {
		return err
	}

	client, err := cf.ShardManagerClient(cmd)
	if err != nil {
		return err
	}

	callCtx, cancel := context.WithTimeout(ctx, cmd.Duration(FlagContextTimeout))
	defer cancel()

	resp, err := client.UndrainShards(callCtx, &types.UndrainShardsRequest{
		Namespace: namespace,
		ShardKeys: shardKeys,
	})
	if err != nil {
		return fmt.Errorf("UndrainShards: %w", err)
	}
	return encodeJSON(out, resp)
}

func runListDrainedShards(ctx context.Context, cmd *cliv3.Command, out io.Writer, cf ClientFactory) error {
	namespace := cmd.String(FlagNamespace)
	if namespace == "" {
		return fmt.Errorf("--%s is required", FlagNamespace)
	}

	client, err := cf.ShardManagerClient(cmd)
	if err != nil {
		return err
	}

	callCtx, cancel := context.WithTimeout(ctx, cmd.Duration(FlagContextTimeout))
	defer cancel()

	resp, err := client.GetDrainedShards(callCtx, &types.GetDrainedShardsRequest{
		Namespace: namespace,
	})
	if err != nil {
		return fmt.Errorf("GetDrainedShards: %w", err)
	}
	return encodeJSON(out, resp)
}

// requireNamespaceAndShards extracts and validates --namespace and --shard from cmd.
func requireNamespaceAndShards(cmd *cliv3.Command) (namespace string, shardKeys []string, err error) {
	namespace = cmd.String(FlagNamespace)
	if namespace == "" {
		return "", nil, fmt.Errorf("--%s is required", FlagNamespace)
	}
	shardKeys = cmd.StringSlice(FlagShardKey)
	if len(shardKeys) == 0 {
		return "", nil, fmt.Errorf("at least one --%s is required", FlagShardKey)
	}
	return namespace, shardKeys, nil
}

func encodeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	return nil
}
