// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package smctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	cliv3 "github.com/urfave/cli/v3"

	"github.com/cadence-workflow/shard-manager/common/types"
)

// namespaceCommand returns the "namespace" command group. All of its
// subcommands operate on the namespace identified by the persistent
// --namespace/-n flag declared on the root smctl command, e.g.:
//
//	smctl -n <namespace> namespace state
//	smctl namespace state -n <namespace>
func namespaceCommand(cf ClientFactory) *cliv3.Command {
	return &cliv3.Command{
		Name:        "namespace",
		Aliases:     []string{"ns"},
		Usage:       "Inspect and manage shard-manager namespaces",
		Description: "Use --namespace/-n on the root command to identify the target namespace.",
		Commands: []*cliv3.Command{
			namespaceStateCommand(cf),
		},
	}
}

// namespaceStateCommand prints the current state of a namespace by calling
// shard-manager's GetNamespaceState API.
func namespaceStateCommand(cf ClientFactory) *cliv3.Command {
	return &cliv3.Command{
		Name:        "state",
		Aliases:     []string{"st"},
		Usage:       "Print the current state of a namespace",
		Description: "Calls GetNamespaceState on shard-manager and prints the response as indented JSON.",
		Action: func(ctx context.Context, cmd *cliv3.Command) error {
			return runGetNamespaceState(ctx, cmd, resolveWriter(cmd), cf)
		},
	}
}

func runGetNamespaceState(
	ctx context.Context,
	cmd *cliv3.Command,
	out io.Writer,
	cf ClientFactory,
) error {
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

	resp, err := client.GetNamespaceState(callCtx, &types.GetNamespaceStateRequest{
		Namespace: namespace,
	})
	if err != nil {
		return fmt.Errorf("GetNamespaceState: %w", err)
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(resp); err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	return nil
}

// resolveWriter returns the writer to use for command output. urfave/cli/v3
// auto-assigns os.Stdout to every subcommand's Writer during setup, so we
// always read from the root command (which is where callers/tests set it).
func resolveWriter(cmd *cliv3.Command) io.Writer {
	if cmd != nil {
		if root := cmd.Root(); root != nil && root.Writer != nil {
			return root.Writer
		}
	}
	return os.Stdout
}
