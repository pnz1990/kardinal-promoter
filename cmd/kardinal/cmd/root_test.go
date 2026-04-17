// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnrichClientError verifies that enrichClientError adds actionable hints
// to known connection failures without changing unknown errors.
func TestEnrichClientError(t *testing.T) {
	cases := []struct {
		name          string
		inputMsg      string
		wantHint      string
		wantUnchanged bool
	}{
		{
			name:     "kubeconfig not found",
			inputMsg: "build kubeconfig: no such file or directory",
			wantHint: "kardinal doctor",
		},
		{
			name:     "context deadline",
			inputMsg: "context deadline exceeded while connecting",
			wantHint: "kubectl cluster-info",
		},
		{
			name:     "forbidden RBAC",
			inputMsg: "Forbidden: access denied",
			wantHint: "RBAC permission denied",
		},
		{
			name:     "CRD not installed",
			inputMsg: "no kind is registered for Pipeline",
			wantHint: "CRDs not installed",
		},
		{
			name:          "unknown error unchanged",
			inputMsg:      "some unknown error",
			wantUnchanged: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := fmt.Errorf("%s", tc.inputMsg) //nolint:err113
			got := enrichClientError(input)
			require.NotNil(t, got)
			if tc.wantUnchanged {
				assert.Equal(t, input.Error(), got.Error())
			} else {
				assert.Contains(t, got.Error(), tc.wantHint)
				assert.Contains(t, got.Error(), tc.inputMsg, "original error must be preserved")
			}
		})
	}
}

// TestEnrichClientError_Nil verifies that nil error is returned unchanged.
func TestEnrichClientError_Nil(t *testing.T) {
	assert.Nil(t, enrichClientError(nil))
}
