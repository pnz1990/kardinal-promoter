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
// GitWatcher uses the Git Smart HTTP protocol (or git-upload-pack over SSH) to
// read branch HEAD SHAs without cloning, and a shallow fetch to match pathGlob.
// HelmWatcher reads an HTTP chart repository's index.yaml or an OCI chart's tags.
// All of them send their requests through the egress guard (pkg/egress).
package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
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

// guardedTransport refuses loopback, link-local, cloud metadata, unspecified
// and multicast destinations at dial time (pkg/egress), on every connection:
// registry and git requests, redirects and token realm fetches. A
// Subscription's registry or repoURL therefore cannot reach the controller's
// own UI API or the node's credential endpoints. Private ranges stay allowed
// for in-cluster registries and git servers. It honours HTTP(S)_PROXY, as the
// default transport did.
var guardedTransport = egress.NewTransport(http.ProxyFromEnvironment)

// newHTTPClient returns the client the watchers use by default: it times out,
// and its transport applies the egress guard.
func newHTTPClient() *http.Client {
	return &http.Client{Timeout: defaultHTTPTimeout, Transport: guardedTransport}
}

// credentialRedirects returns c with a redirect policy for requests that
// carry credentials: a redirect from https to http is refused, and so is a
// redirect to another host, so the credentials go only where the
// Subscription points. blobsMayLeave allows a cross-host redirect of a
// registry blob download (registries send those to object storage) after
// dropping the Authorization header.
func credentialRedirects(c *http.Client, blobsMayLeave bool) *http.Client {
	cc := *c
	cc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		first := via[0]
		if first.URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("refusing a redirect from https to %s while sending credentials", req.URL.Scheme)
		}
		if req.URL.Host != first.URL.Host {
			if blobsMayLeave && req.Method == http.MethodGet && strings.Contains(first.URL.Path, "/blobs/") {
				req.Header.Del("Authorization")
				return nil
			}
			return fmt.Errorf("refusing a redirect to another host (%s) while sending credentials", req.URL.Hostname())
		}
		return nil
	}
	return &cc
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
	// Tag is a human-readable label (image tag, short commit SHA or chart
	// version).
	Tag string
	// Revision is the source position the poll read up to when it differs
	// from Digest: the branch head of a pathGlob Git poll, whose Digest is
	// the newest matching commit. Empty otherwise.
	Revision string
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
