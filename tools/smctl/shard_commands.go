package smctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	cliv3 "github.com/urfave/cli/v3"

	"github.com/cadence-workflow/shard-manager/common/types"
)

func shardCommand(cf ClientFactory) *cliv3.Command {
	return &cliv3.Command{
		Name:        "shard",
		Aliases:     []string{"sh"},
		Usage:       "Inspect and manage shard-manager shards",
		Description: "Use --shard/-sd on the root command to identify the target shard.",
		Commands: []*cliv3.Command{
			shardOwnerCommand(cf),
		},
	}
}

// shardOwnerCommand prints the owner of the given shard by calling
// shard-manager's GetShardOwner API.
func shardOwnerCommand(cf ClientFactory) *cliv3.Command {
	return &cliv3.Command{
		Name:        "get-owner",
		Aliases:     []string{"o"},
		Usage:       "Get the executor that currently owns a given shard",
		Description: "Calls GetShardOwner on shard-manager and prints the response.",
		Flags: []cliv3.Flag{
			&cliv3.StringFlag{
				Name:    FlagShardKey,
				Aliases: []string{"sk"},
				Usage:   "shard key to look up",
			},
		},
		Action: func(ctx context.Context, cmd *cliv3.Command) error {
			return runGetShardOwner(ctx, cmd, resolveWriter(cmd), cf)
		},
	}
}

func runGetShardOwner(
	ctx context.Context,
	cmd *cliv3.Command,
	out io.Writer,
	cf ClientFactory,
) error {
	namespace := cmd.String(FlagNamespace)
	if namespace == "" {
		return fmt.Errorf("--%s is required", FlagNamespace)
	}

	shardKey := cmd.String(FlagShardKey)
	if shardKey == "" {
		return fmt.Errorf("--%s is required", FlagShardKey)
	}

	client, err := cf.ShardManagerClient(cmd)
	if err != nil {
		return err
	}

	callCtx, cancel := context.WithTimeout(ctx, cmd.Duration(FlagContextTimeout))
	defer cancel()

	resp, err := client.GetShardOwner(callCtx, &types.GetShardOwnerRequest{
		Namespace: namespace,
		ShardKey:  shardKey,
	})
	if err != nil {
		return fmt.Errorf("GetShardOwner: %w", err)
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(resp); err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	return nil
}
