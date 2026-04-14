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

// Package source provides interfaces and implementations for watching artifact sources.
//
// The Watcher interface is the pluggable integration point for artifact discovery.
// OCIWatcher uses the OCI Distribution Specification API to list tags and read digests.
// GitWatcher uses the Git Smart HTTP protocol to read branch HEAD SHAs without cloning.
package source

import "context"

// WatchResult is the result of polling an artifact source.
type WatchResult struct {
	// Digest is the unique identifier of the latest artifact:
	//   - OCI image: the full digest (e.g. "sha256:abc123...")
	//   - Git commit: the full SHA (e.g. "abc1234...")
	Digest string
	// Tag is a human-readable label (image tag or short commit SHA).
	Tag string
	// Changed is true when Digest differs from the last known digest.
	Changed bool
}

// Watcher is the interface for artifact source watchers.
// Implementations must be safe for concurrent use.
type Watcher interface {
	// Watch polls the artifact source and returns the latest artifact.
	// Returns an error if the source cannot be reached.
	// Watch is idempotent: calling it multiple times for the same digest returns
	// WatchResult{Changed: false}.
	Watch(ctx context.Context, lastDigest string) (*WatchResult, error)
}
