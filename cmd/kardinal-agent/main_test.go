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
	"context"
	"strings"
	"testing"

	"github.com/rs/zerolog"
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
		// C07-controller-19: values that can never match a kardinal.io/shard
		// label were accepted, so the agent started and claimed nothing.
		{name: "shard with a space is rejected", shard: "eu cluster", wantErr: true},
		{name: "shard with a slash is rejected", shard: "prod/us", wantErr: true},
		{name: "newline-only shard is rejected", shard: "\n", wantErr: true},
		{name: "shard longer than 63 characters is rejected", shard: strings.Repeat("a", 70), wantErr: true},
		{name: "63-character shard is valid", shard: strings.Repeat("a", 63), wantErr: false},
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

// TestNewAgentLogger covers C07-controller-18: the agent never set
// zerolog.DefaultContextLogger, so reconciler logs written through
// zerolog.Ctx(ctx) went to a disabled logger.
func TestNewAgentLogger(t *testing.T) {
	prevLevel, prevDefault := zerolog.GlobalLevel(), zerolog.DefaultContextLogger
	t.Cleanup(func() {
		zerolog.SetGlobalLevel(prevLevel)
		zerolog.DefaultContextLogger = prevDefault
	})

	tests := []struct {
		level string
		want  zerolog.Level
	}{
		{level: "debug", want: zerolog.DebugLevel},
		{level: "not-a-level", want: zerolog.InfoLevel},
	}
	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			zerolog.DefaultContextLogger = nil
			newAgentLogger(tt.level)
			assert.Equal(t, tt.want, zerolog.GlobalLevel())
			assert.NotEqual(t, zerolog.Disabled, zerolog.Ctx(context.Background()).GetLevel(),
				"reconcilers log through zerolog.Ctx(ctx); it must not be the disabled logger")
		})
	}
}
