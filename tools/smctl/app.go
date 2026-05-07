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

// Package smctl defines the shard-manager CLI application (smctl).
package smctl

import (
	"context"
	"fmt"

	cliv3 "github.com/urfave/cli/v3"

	"github.com/cadence-workflow/shard-manager/common/metrics"
)

// BuildCommand returns the root smctl command wired with ClientFactory.
func BuildCommand() *cliv3.Command {
	return BuildCommandWithFactory(NewClientFactory())
}

// BuildCommandWithFactory returns the root smctl command using the provided
// ClientFactory.
func BuildCommandWithFactory(cf ClientFactory) *cliv3.Command {
	return &cliv3.Command{
		Name:        "smctl",
		Usage:       "Command-line client for shard-manager",
		Description: "Inspect and manage shard-manager namespaces.",
		Version:     fmt.Sprintf("%s (commit %s)", metrics.ReleaseVersion, metrics.Revision),
		Flags:       rootFlags(),
		Commands: []*cliv3.Command{
			namespaceCommand(cf),
		},
		// After is invoked even when the action returns an error, so it is the
		// right place to release the dispatcher built on first command use.
		After: func(_ context.Context, _ *cliv3.Command) error {
			return cf.Close()
		},
	}
}

// rootFlags returns the flags exposed on the smctl root command.
// These are persistent (Local==false) so subcommands can read them regardless of
// where the flag was supplied on the command line.
//
// The package imports github.com/urfave/cli/v3 under the alias cliv3 (not the
// default cli) so that gopls' organize-imports does not silently drop the
// import: github.com/urfave/cli/v2 is also in this go.mod and exports the same
// package name `cli`, which makes bare cli.X references ambiguous and triggers
// gopls to rewrite the import. cliv3.X is unambiguous.
func rootFlags() []cliv3.Flag {
	return []cliv3.Flag{
		&cliv3.StringFlag{
			Name:    FlagNamespace,
			Aliases: []string{"n"},
			Usage:   "namespace to operate on (required by namespace subcommands)",
			Sources: cliv3.EnvVars("SMCTL_NAMESPACE"),
		},
		&cliv3.StringFlag{
			Name:    FlagAddress,
			Aliases: []string{"ad"},
			Usage:   "host:port for shard-manager service (default: " + grpcDefaultAddress + ")",
			Sources: cliv3.EnvVars("SMCTL_ADDRESS"),
		},
		&cliv3.StringFlag{
			Name:    FlagTransport,
			Aliases: []string{"t"},
			Usage:   "transport protocol; only 'grpc' is currently supported",
			Value:   grpcTransport,
			Sources: cliv3.EnvVars("SMCTL_TRANSPORT"),
		},
		&cliv3.StringFlag{
			Name:    FlagTLSCertPath,
			Aliases: []string{"tcp"},
			Usage:   "path to TLS server CA certificate; enables TLS when set",
			Sources: cliv3.EnvVars("SMCTL_TLS_CERT_PATH"),
		},
		&cliv3.DurationFlag{
			Name:    FlagContextTimeout,
			Aliases: []string{"ct"},
			Usage:   "timeout for each RPC call",
			Value:   defaultContextTimeout,
			Sources: cliv3.EnvVars("SMCTL_CONTEXT_TIMEOUT"),
		},
	}
}
