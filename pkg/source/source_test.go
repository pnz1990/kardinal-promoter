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

package source_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

// --- Git Watcher Tests ---

// makePktLine returns a git pkt-line encoded message.
// Format: 4 hex chars of (length + 4) + payload + newline.
func makePktLine(s string) string {
	length := len(s) + 4 + 1 // +4 for prefix, +1 for newline
	return fmt.Sprintf("%04x%s\n", length, s)
}

// makeGitRefsResponse constructs a minimal git Smart HTTP info/refs response
// with the given refs map (refName → sha).
func makeGitRefsResponse(refs map[string]string) string {
	var sb string
	// Service announcement
	sb += makePktLine("# service=git-upload-pack")
	sb += "0000" // flush

	first := true
	for ref, sha := range refs {
		if first {
			// First ref line includes capability advertisement after NUL byte.
			sb += makePktLine(sha + " " + ref + "\x00 side-band-64k")
			first = false
		} else {
			sb += makePktLine(sha + " " + ref)
		}
	}
	sb += "0000" // flush
	return sb
}

// TestGitWatcher_DetectsNewCommit verifies that Watch returns Changed=true
// when the SHA differs from the last known digest.
func TestGitWatcher_DetectsNewCommit(t *testing.T) {
	const newSHA = "abc1234567890123456789012345678901234567a"
	const oldSHA = "def0000000000000000000000000000000000000"

	body := makeGitRefsResponse(map[string]string{
		"refs/heads/main": newSHA,
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "service=git-upload-pack")
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		fmt.Fprint(w, body) //nolint:errcheck
	}))
	defer srv.Close()

	watcher := source.NewGitWatcher(srv.URL, "main", "")
	result, err := watcher.Watch(context.Background(), oldSHA)
	require.NoError(t, err)

	assert.True(t, result.Changed, "Changed must be true when SHA differs")
	assert.Equal(t, newSHA, result.Digest)
	assert.Equal(t, "abc1234", result.Tag, "Tag must be the short SHA (7 chars)")
}

// TestGitWatcher_NoChangeWhenSHAUnchanged verifies that Watch returns Changed=false
// when the repo SHA is the same as lastDigest.
func TestGitWatcher_NoChangeWhenSHAUnchanged(t *testing.T) {
	const sha = "abc1234567890123456789012345678901234567a"

	body := makeGitRefsResponse(map[string]string{
		"refs/heads/main": sha,
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body) //nolint:errcheck
	}))
	defer srv.Close()

	watcher := source.NewGitWatcher(srv.URL, "main", "")
	result, err := watcher.Watch(context.Background(), sha)
	require.NoError(t, err)

	assert.False(t, result.Changed, "Changed must be false when SHA is unchanged")
	assert.Equal(t, sha, result.Digest)
}

// TestGitWatcher_FirstRunNotChanged verifies that first-run (lastDigest="")
// does NOT return Changed=true to avoid spurious Bundle creation on startup.
func TestGitWatcher_FirstRunNotChanged(t *testing.T) {
	const sha = "abc1234567890123456789012345678901234567a"

	body := makeGitRefsResponse(map[string]string{
		"refs/heads/main": sha,
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body) //nolint:errcheck
	}))
	defer srv.Close()

	watcher := source.NewGitWatcher(srv.URL, "main", "")
	result, err := watcher.Watch(context.Background(), "") // first run
	require.NoError(t, err)

	assert.False(t, result.Changed, "First run must not return Changed=true")
	assert.Equal(t, sha, result.Digest, "Digest must be set even on first run")
}

// TestGitWatcher_BranchNotFound verifies that Watch returns Changed=false
// with the previous digest when the branch is not in the refs advertisement.
func TestGitWatcher_BranchNotFound(t *testing.T) {
	const oldSHA = "def0000000000000000000000000000000000000"

	// Only "refs/heads/other" is advertised, not "refs/heads/main".
	body := makeGitRefsResponse(map[string]string{
		"refs/heads/other": "aaa0000000000000000000000000000000000000",
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body) //nolint:errcheck
	}))
	defer srv.Close()

	watcher := source.NewGitWatcher(srv.URL, "main", "")
	result, err := watcher.Watch(context.Background(), oldSHA)
	require.NoError(t, err)

	assert.False(t, result.Changed, "Changed must be false when branch not found")
	assert.Equal(t, oldSHA, result.Digest, "Digest must be previous value when branch not found")
}

// TestGitWatcher_ServerError verifies that Watch returns an error on HTTP failure.
func TestGitWatcher_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	watcher := source.NewGitWatcher(srv.URL, "main", "")
	_, err := watcher.Watch(context.Background(), "")
	assert.Error(t, err, "Watch must return error on server error")
	assert.Contains(t, err.Error(), "500")
}

// TestGitWatcher_EmptyRepoURL verifies validation.
func TestGitWatcher_EmptyRepoURL(t *testing.T) {
	watcher := source.NewGitWatcher("", "main", "")
	_, err := watcher.Watch(context.Background(), "")
	assert.Error(t, err)
}

// --- OCI Watcher Tests ---
// OCI watcher tests use httptest to mock the OCI registry API.

// fakeRegistry creates a test HTTP server simulating an OCI registry.
// It serves:
//   - GET /v2/<name>/tags/list — returns the given tags
//   - HEAD /v2/<name>/manifests/<tag> — returns a fake digest header
func fakeRegistry(t *testing.T, imageName string, tags []string, tagToDigest map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/"+imageName+"/tags/list":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"name":%q,"tags":%s}`, imageName, toJSONArray(tags)) //nolint:errcheck
		case r.Method == http.MethodHead:
			// Extract tag from path: /v2/<name>/manifests/<tag>
			parts := splitPath(r.URL.Path)
			tag := parts[len(parts)-1]
			digest, ok := tagToDigest[tag]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	return srv
}

func toJSONArray(ss []string) string {
	if len(ss) == 0 {
		return "[]"
	}
	out := "["
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%q", s)
	}
	out += "]"
	return out
}

func splitPath(p string) []string {
	var parts []string
	for _, seg := range splitSlash(p) {
		if seg != "" {
			parts = append(parts, seg)
		}
	}
	return parts
}

func splitSlash(s string) []string {
	var result []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '/' {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	return result
}

// TestOCIWatcher_DetectsNewTag verifies that Watch returns Changed=true
// when the registry has a new tag with a different digest.
func TestOCIWatcher_DetectsNewTag(t *testing.T) {
	const imageName = "myorg/myapp"
	const newDigest = "sha256:abc1234567890abcdef"
	const oldDigest = "sha256:000000000000000000"

	srv := fakeRegistry(t, imageName, []string{"sha-aaa111", "sha-bbb222"}, map[string]string{
		"sha-aaa111": "sha256:old111",
		"sha-bbb222": newDigest,
	})
	defer srv.Close()

	// The registry host is srv.URL (without https://).
	// go-containerregistry expects "host/name" format for non-default registries.
	registryRef := srv.URL + "/" + imageName

	watcher := source.NewOCIWatcher(registryRef, "^sha-")
	result, err := watcher.Watch(context.Background(), oldDigest)
	require.NoError(t, err)

	// sha-bbb222 sorts after sha-aaa111, so it's the "newest".
	assert.True(t, result.Changed, "Changed must be true when digest differs")
	assert.Equal(t, newDigest, result.Digest)
	assert.Equal(t, "sha-bbb222", result.Tag)
}

// TestOCIWatcher_NoChangeWhenDigestUnchanged verifies that Watch returns
// Changed=false when the newest tag digest matches lastDigest.
func TestOCIWatcher_NoChangeWhenDigestUnchanged(t *testing.T) {
	const imageName = "myorg/myapp"
	const digest = "sha256:abc1234567890abcdef"

	srv := fakeRegistry(t, imageName, []string{"sha-aaa111"}, map[string]string{
		"sha-aaa111": digest,
	})
	defer srv.Close()

	registryRef := srv.URL + "/" + imageName
	watcher := source.NewOCIWatcher(registryRef, "")
	result, err := watcher.Watch(context.Background(), digest)
	require.NoError(t, err)

	assert.False(t, result.Changed)
	assert.Equal(t, digest, result.Digest)
}

// TestOCIWatcher_FirstRunNotChanged verifies that first-run (lastDigest="")
// does NOT return Changed=true.
func TestOCIWatcher_FirstRunNotChanged(t *testing.T) {
	const imageName = "myorg/myapp"
	const digest = "sha256:abc1234567890abcdef"

	srv := fakeRegistry(t, imageName, []string{"latest"}, map[string]string{
		"latest": digest,
	})
	defer srv.Close()

	registryRef := srv.URL + "/" + imageName
	watcher := source.NewOCIWatcher(registryRef, "")
	result, err := watcher.Watch(context.Background(), "")
	require.NoError(t, err)

	assert.False(t, result.Changed, "First run must not return Changed=true")
	assert.Equal(t, digest, result.Digest)
}

// TestOCIWatcher_TagFilterApplied verifies that only tags matching TagFilter are considered.
func TestOCIWatcher_TagFilterApplied(t *testing.T) {
	const imageName = "myorg/myapp"

	srv := fakeRegistry(t, imageName, []string{"latest", "sha-aaa111", "sha-bbb222"}, map[string]string{
		"latest":     "sha256:old-latest",
		"sha-aaa111": "sha256:sha-aaa-digest",
		"sha-bbb222": "sha256:sha-bbb-digest",
	})
	defer srv.Close()

	registryRef := srv.URL + "/" + imageName
	// TagFilter matches only sha-* tags.
	watcher := source.NewOCIWatcher(registryRef, "^sha-")
	result, err := watcher.Watch(context.Background(), "sha256:old")
	require.NoError(t, err)

	// "latest" must be excluded by filter.
	// sha-bbb222 sorts after sha-aaa111.
	assert.Equal(t, "sha-bbb222", result.Tag, "Only sha- tags should be considered")
	assert.Equal(t, "sha256:sha-bbb-digest", result.Digest)
}

// TestOCIWatcher_NoMatchingTagsReturnsNotChanged verifies behavior when
// no tags match the filter.
func TestOCIWatcher_NoMatchingTagsReturnsNotChanged(t *testing.T) {
	const imageName = "myorg/myapp"
	const oldDigest = "sha256:prev"

	srv := fakeRegistry(t, imageName, []string{"v1.0.0", "v1.1.0"}, map[string]string{})
	defer srv.Close()

	registryRef := srv.URL + "/" + imageName
	watcher := source.NewOCIWatcher(registryRef, "^sha-")
	result, err := watcher.Watch(context.Background(), oldDigest)
	require.NoError(t, err)

	assert.False(t, result.Changed)
	assert.Equal(t, oldDigest, result.Digest, "Digest must be previous when no tags match")
}

// TestOCIWatcher_InvalidTagFilterReturnsError verifies that an invalid regex is rejected.
func TestOCIWatcher_InvalidTagFilterReturnsError(t *testing.T) {
	const imageName = "myorg/myapp"

	srv := fakeRegistry(t, imageName, []string{"v1.0"}, map[string]string{"v1.0": "sha256:abc"})
	defer srv.Close()

	registryRef := srv.URL + "/" + imageName
	watcher := source.NewOCIWatcher(registryRef, "[invalid")
	_, err := watcher.Watch(context.Background(), "")
	assert.Error(t, err, "invalid regex must return error")
}

// TestOCIWatcher_EmptyRegistryReturnsError verifies validation.
func TestOCIWatcher_EmptyRegistryReturnsError(t *testing.T) {
	watcher := source.NewOCIWatcher("", "")
	_, err := watcher.Watch(context.Background(), "")
	assert.Error(t, err)
}
