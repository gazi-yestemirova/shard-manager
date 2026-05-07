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

//go:generate mockgen -package $GOPACKAGE -source $GOFILE -destination authorizer_mock.go -self_package github.com/cadence-workflow/shard-manager/common/authorization

// Package authorization defines the Authorizer interface used by shard-distributor
// RPC wrappers to enforce per-API permissions. The default implementation is a
// no-op that allows every request; deployments can inject an alternative
// implementation via fx.
package authorization

import "context"

const (
	// DecisionDeny means auth decision is denied.
	DecisionDeny Decision = iota + 1
	// DecisionAllow means auth decision is allowed.
	DecisionAllow
)

const (
	// PermissionRead means the caller can invoke read-only namespace APIs.
	PermissionRead Permission = iota + 1
	// PermissionWrite means the caller can invoke namespace mutation APIs.
	PermissionWrite
	// PermissionAdmin means the caller can invoke admin-only APIs.
	PermissionAdmin
)

type (
	// Decision is the enum type for an auth decision.
	Decision int

	// Permission is the enum type for the permission required by an API.
	Permission int

	// Attributes is the input the Authorizer makes its decision on.
	// Fields are intentionally minimal; extend (e.g., with Actor, RequestBody)
	// when a non-nop authorizer needs them.
	Attributes struct {
		APIName    string
		Namespace  string
		Permission Permission
	}

	// Result is the output of an authorization decision.
	Result struct {
		Decision Decision
	}

	// Authorizer evaluates whether a request described by Attributes is allowed.
	Authorizer interface {
		Authorize(ctx context.Context, attributes *Attributes) (Result, error)
	}
)
