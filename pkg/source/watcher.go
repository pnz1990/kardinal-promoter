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

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// defaultHTTPTimeout bounds every watcher request, so a hung registry or git
	// host cannot block the Subscription reconcile worker.
	defaultHTTPTimeout = 30 * time.Second
	// maxResponseBytes bounds a registry response body (tags/list page, manifest,
	// image config).
	maxResponseBytes = 4 << 20
	// maxTokenBytes bounds an anonymous token response.
	maxTokenBytes = 64 << 10
	// maxRefsBytes bounds a git info/refs advertisement.
	maxRefsBytes = 32 << 20
	userAgent    = "kardinal-promoter/subscription-watcher"
)

// newHTTPClient returns the client the watchers use by default.
func newHTTPClient() *http.Client {
	return &http.Client{Timeout: defaultHTTPTimeout}
}

// readLimited reads r fully and fails when it is longer than limit bytes.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return b, nil
}

// drainClose discards a small remainder of the body and closes it, so the
// connection can be reused.
func drainClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}

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
