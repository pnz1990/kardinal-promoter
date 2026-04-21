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

// Package main (controller) — namespace-scoped mode cache options test (spec issue-983).
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

// buildCacheOpts replicates the cache option logic from main() for unit testing.
// This allows testing the namespace-scoped mode without starting a real manager.
func buildCacheOpts(watchNamespace string) cache.Options {
	opts := cache.Options{}
	if watchNamespace != "" {
		opts.DefaultNamespaces = map[string]cache.Config{watchNamespace: {}}
	}
	return opts
}

// TestWatchNamespaceFlagParsed verifies that when watchNamespace is set, the
// cache options include the namespace in DefaultNamespaces (spec O1).
func TestWatchNamespaceFlagParsed(t *testing.T) {
	tests := []struct {
		name              string
		watchNamespace    string
		wantDefaultNS     bool
		wantNamespaceName string
	}{
		{
			name:           "empty (cluster-wide mode)",
			watchNamespace: "",
			wantDefaultNS:  false,
		},
		{
			name:              "single namespace set",
			watchNamespace:    "my-team-ns",
			wantDefaultNS:     true,
			wantNamespaceName: "my-team-ns",
		},
		{
			name:              "kardinal-system namespace",
			watchNamespace:    "kardinal-system",
			wantDefaultNS:     true,
			wantNamespaceName: "kardinal-system",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := buildCacheOpts(tc.watchNamespace)

			if !tc.wantDefaultNS {
				assert.Nil(t, opts.DefaultNamespaces,
					"cluster-wide mode must not set DefaultNamespaces")
				return
			}

			require.NotNil(t, opts.DefaultNamespaces,
				"namespace-scoped mode must set DefaultNamespaces")
			assert.Len(t, opts.DefaultNamespaces, 1,
				"exactly one namespace must be in DefaultNamespaces")
			_, ok := opts.DefaultNamespaces[tc.wantNamespaceName]
			assert.True(t, ok,
				"namespace %q must be in DefaultNamespaces", tc.wantNamespaceName)
		})
	}
}

// TestWatchNamespaceDefaultEmpty verifies that the default (no KARDINAL_WATCH_NAMESPACE)
// results in cluster-wide mode (O4 backward compatibility).
func TestWatchNamespaceDefaultEmpty(t *testing.T) {
	opts := buildCacheOpts("")
	assert.Nil(t, opts.DefaultNamespaces,
		"default (empty watchNamespace) must produce cluster-wide cache with nil DefaultNamespaces")
}
