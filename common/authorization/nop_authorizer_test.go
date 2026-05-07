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

package authorization

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNopAuthorizer_AllowsEveryRequest(t *testing.T) {
	tests := []struct {
		name  string
		attrs *Attributes
	}{
		{
			name:  "nil attributes",
			attrs: nil,
		},
		{
			name:  "empty attributes",
			attrs: &Attributes{},
		},
		{
			name: "read on namespace",
			attrs: &Attributes{
				APIName:    "GetNamespaceState",
				Namespace:  "ns1",
				Permission: PermissionRead,
			},
		},
		{
			name: "admin without namespace",
			attrs: &Attributes{
				APIName:    "AdminAPI",
				Permission: PermissionAdmin,
			},
		},
	}

	authz := NewNopAuthorizer()
	require.NotNil(t, authz)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := authz.Authorize(context.Background(), tc.attrs)
			require.NoError(t, err)
			assert.Equal(t, DecisionAllow, result.Decision)
		})
	}
}

func TestPermission_DistinctValues(t *testing.T) {
	assert.NotEqual(t, PermissionRead, PermissionWrite)
	assert.NotEqual(t, PermissionRead, PermissionAdmin)
	assert.NotEqual(t, PermissionWrite, PermissionAdmin)
}

func TestDecision_DistinctValues(t *testing.T) {
	assert.NotEqual(t, DecisionAllow, DecisionDeny)
}
