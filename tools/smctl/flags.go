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

import "time"

// Flag names used by smctl. Connection flags are persistent on the root
// command so every subcommand inherits them, mirroring how the Cadence CLI
// exposes --address / --transport / --tls-cert-path on its root app.
const (
	FlagAddress        = "address"
	FlagTransport      = "transport"
	FlagTLSCertPath    = "tls-cert-path"
	FlagContextTimeout = "context-timeout"

	FlagNamespace = "namespace"
)

// Connection defaults for talking to a locally-running shard-manager.
const (
	// smctlClientName is the YARPC caller name used by smctl. Mirrors how the
	smctlClientName = "shard-manager-cli"

	// shardManagerServiceName is the YARPC service identifier the smctl
	// dispatcher targets. The *value* must remain "shard-distributor" because
	// that is the name the server registers under (see common/service/name.go
	// `ShardDistributor = "shard-distributor"` and the production yaml
	// `service.aliases: [shard-distributor]`). The smctl-side identifier is
	// named after the umbrella shard-manager project for readability.
	shardManagerServiceName = "shard-distributor"

	// grpcDefaultAddress is the default host:port for shard-manager's gRPC
	// inbound (see config/development.yaml: rpc.grpcPort=7943).
	grpcDefaultAddress = "127.0.0.1:7943"

	// grpcTransport is the only transport currently supported. The flag is
	// kept for parity with the Cadence CLI so future tchannel/etc. variants
	// have a place to plug in.
	grpcTransport = "grpc"

	defaultContextTimeout = 10 * time.Second
)
