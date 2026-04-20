// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValidateShard verifies that an empty shard returns an error and a non-empty
// shard returns nil.
//
// Design ref: docs/design/07-distributed-architecture.md §Agent filtering
// Spec O3: --shard is required; empty shard must return an error.
func TestValidateShard(t *testing.T) {
	tests := []struct {
		name    string
		shard   string
		wantErr bool
	}{
		{
			name:    "empty shard returns error",
			shard:   "",
			wantErr: true,
		},
		{
			name:    "whitespace-only shard returns error",
			shard:   "   ",
			wantErr: true,
		},
		{
			name:    "valid shard returns nil",
			shard:   "eu-cluster",
			wantErr: false,
		},
		{
			name:    "shard with hyphens is valid",
			shard:   "prod-us-east-1",
			wantErr: false,
		},
		{
			name:    "single character shard is valid",
			shard:   "a",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateShard(tc.shard)
			if tc.wantErr {
				assert.Error(t, err, "expected error for shard %q", tc.shard)
			} else {
				assert.NoError(t, err, "expected no error for shard %q", tc.shard)
			}
		})
	}
}

// TestErrShardRequired verifies the error message is informative.
func TestErrShardRequired(t *testing.T) {
	err := validateShard("")
	assert.Contains(t, err.Error(), "--shard is required")
}
