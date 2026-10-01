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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
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

	watcher := source.NewGitWatcher(srv.URL, "main", "").WithHTTPClient(srv.Client())
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

	watcher := source.NewGitWatcher(srv.URL, "main", "").WithHTTPClient(srv.Client())
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

	watcher := source.NewGitWatcher(srv.URL, "main", "").WithHTTPClient(srv.Client())
	result, err := watcher.Watch(context.Background(), "") // first run
	require.NoError(t, err)

	assert.False(t, result.Changed, "First run must not return Changed=true")
	assert.Equal(t, sha, result.Digest, "Digest must be set even on first run")
}

// TestGitWatcher_BranchNotFound verifies that a branch missing from the refs
// advertisement is an error, not a silent "no change" (C05-steps-05).
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

	watcher := source.NewGitWatcher(srv.URL, "main", "").WithHTTPClient(srv.Client())
	_, err := watcher.Watch(context.Background(), oldSHA)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `branch "main" not found`)
	assert.Contains(t, err.Error(), "1 refs advertised")
}

// TestGitWatcher_EmptyAdvertisementIsAnError covers a server that answers with
// no refs at all, for example a protocol v2 capability advertisement.
func TestGitWatcher_EmptyAdvertisementIsAnError(t *testing.T) {
	body := makePktLine("# service=git-upload-pack") + "0000" +
		makePktLine("version 2") + makePktLine("ls-refs=unborn") + "0000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body) //nolint:errcheck
	}))
	defer srv.Close()

	_, err := source.NewGitWatcher(srv.URL, "main", "").WithHTTPClient(srv.Client()).Watch(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "advertises no refs")
}

// TestGitWatcher_TruncatedPacketIsAnError verifies that a read error inside a
// packet is returned instead of being reported as "branch not found" (C05-steps-20).
func TestGitWatcher_TruncatedPacketIsAnError(t *testing.T) {
	body := makePktLine("# service=git-upload-pack") + "0000" + "0045" + strings.Repeat("a", 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body) //nolint:errcheck
	}))
	defer srv.Close()

	_, err := source.NewGitWatcher(srv.URL, "main", "").WithHTTPClient(srv.Client()).Watch(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read pkt-line payload")
}

// TestGitWatcher_ShortReads delivers the advertisement in 3-byte chunks. The
// parser must return the same SHA as for a single write (C05-steps-20).
func TestGitWatcher_ShortReads(t *testing.T) {
	sha := strings.Repeat("ab", 20)
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	body := pkt("# service=git-upload-pack\n") + "0000" +
		pkt(strings.Repeat("cd", 20)+" HEAD\x00multi_ack side-band-64k\n") +
		pkt(strings.Repeat("ef", 20)+" refs/heads/feature-with-a-long-name\n") +
		pkt(sha+" refs/heads/main\n") + "0000"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f := w.(http.Flusher)
		for i := 0; i < len(body); i += 3 {
			end := min(i+3, len(body))
			_, _ = w.Write([]byte(body[i:end]))
			f.Flush()
			time.Sleep(time.Millisecond)
		}
	}))
	defer srv.Close()

	r, err := source.NewGitWatcher(srv.URL, "main", "").WithHTTPClient(srv.Client()).Watch(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, sha, r.Digest)
}

// TestGitWatcher_PathGlobIsRejected verifies that the unimplemented pathGlob is
// reported instead of being ignored (C05-steps-27).
func TestGitWatcher_PathGlobIsRejected(t *testing.T) {
	_, err := source.NewGitWatcher("https://example.invalid/repo", "main", "config/**").Watch(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path filtering is not implemented")
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// TestGitWatcher_RealGitHTTPBackend polls a real bare repository served by
// git http-backend. With the old "Git-Protocol: version=2" header the server
// answered with no refs and the watcher never saw a commit (C05-steps-05).
func TestGitWatcher_RealGitHTTPBackend(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	require.NoError(t, os.MkdirAll(work, 0o755))
	runGit(t, work, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte("1"), 0o600))
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "-q", "-m", "one")
	bare := filepath.Join(root, "repo.git")
	runGit(t, root, "clone", "-q", "--bare", work, bare)
	runGit(t, work, "remote", "add", "origin", bare)

	backend := filepath.Join(runGit(t, root, "--exec-path"), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not installed")
	}
	h := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	var mu sync.Mutex
	var protocolHeaders []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		protocolHeaders = append(protocolHeaders, r.Header.Get("Git-Protocol"))
		mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()

	watcher := source.NewGitWatcher(srv.URL+"/repo.git", "main", "").WithHTTPClient(srv.Client())
	first := runGit(t, work, "rev-parse", "HEAD")
	r1, err := watcher.Watch(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, first, r1.Digest)
	assert.False(t, r1.Changed)

	require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte("2"), 0o600))
	runGit(t, work, "commit", "-q", "-am", "two")
	runGit(t, work, "push", "-q", "origin", "main")
	second := runGit(t, work, "rev-parse", "HEAD")

	r2, err := watcher.Watch(context.Background(), r1.Digest)
	require.NoError(t, err)
	assert.True(t, r2.Changed)
	assert.Equal(t, second, r2.Digest)

	_, err = source.NewGitWatcher(srv.URL+"/repo.git", "nope", "").WithHTTPClient(srv.Client()).Watch(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `branch "nope" not found`)

	mu.Lock()
	defer mu.Unlock()
	for _, h := range protocolHeaders {
		assert.Empty(t, h, "the watcher must not request protocol v2")
	}
}

// TestGitWatcher_ServerError verifies that Watch returns an error on HTTP failure.
func TestGitWatcher_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	watcher := source.NewGitWatcher(srv.URL, "main", "").WithHTTPClient(srv.Client())
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
// OCI watcher tests use httptest to mock the OCI Distribution API.

const testToken = "anonymous-test-token"

// testImage is one tag in the fake registry.
type testImage struct {
	digest  string // manifest (or index) digest
	created string // image config "created"
	index   bool   // serve the tag as a multi-arch OCI index
}

// testRegistry is a fake OCI registry. It serves tags/list (optionally
// paginated), manifests by tag and digest (HEAD and GET), config blobs, and
// optionally the anonymous Bearer token flow.
type testRegistry struct {
	name           string
	tags           []string
	images         map[string]testImage
	pageSize       int
	auth           string // "", "bearer" or "basic"
	noDigestHeader bool

	mu            sync.Mutex
	tokenRequests int
	tokenQueries  []string
	tokenAuthz    []string
}

func (reg *testRegistry) start(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			reg.mu.Lock()
			reg.tokenRequests++
			reg.tokenQueries = append(reg.tokenQueries, r.URL.RawQuery)
			reg.tokenAuthz = append(reg.tokenAuthz, r.Header.Get("Authorization"))
			reg.mu.Unlock()
			fmt.Fprintf(w, `{"token":%q}`, testToken) //nolint:errcheck
			return
		}
		switch reg.auth {
		case "bearer":
			if r.Header.Get("Authorization") != "Bearer "+testToken {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(
					`Bearer realm="%s/token",service="test-registry",scope="repository:%s:pull"`, srv.URL, reg.name))
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		case "basic":
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		prefix := "/v2/" + reg.name + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.Error(w, "unknown repository", http.StatusNotFound)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		switch {
		case rest == "tags/list":
			reg.serveTags(w, r)
		case strings.HasPrefix(rest, "manifests/"):
			reg.serveManifest(w, r, strings.TrimPrefix(rest, "manifests/"))
		case strings.HasPrefix(rest, "blobs/sha256:cfg-"):
			img := reg.images[strings.TrimPrefix(rest, "blobs/sha256:cfg-")]
			fmt.Fprintf(w, `{"architecture":"amd64","created":%q}`, img.created) //nolint:errcheck
		default:
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (reg *testRegistry) serveTags(w http.ResponseWriter, r *http.Request) {
	tags := reg.tags
	if reg.pageSize > 0 {
		start := 0
		if last := r.URL.Query().Get("last"); last != "" {
			for i, tag := range tags {
				if tag == last {
					start = i + 1
				}
			}
		}
		end := min(start+reg.pageSize, len(tags))
		if end < len(tags) {
			w.Header().Set("Link", fmt.Sprintf(`</v2/%s/tags/list?last=%s&n=%d>; rel="next"`,
				reg.name, tags[end-1], reg.pageSize))
		}
		tags = tags[start:end]
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"name":%q,"tags":%s}`, reg.name, toJSONArray(tags)) //nolint:errcheck
}

func (reg *testRegistry) serveManifest(w http.ResponseWriter, r *http.Request, ref string) {
	var body, digest string
	if tag, ok := strings.CutPrefix(ref, "sha256:child-"); ok {
		body = fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":"sha256:cfg-%s"}}`, tag)
		digest = ref
	} else {
		img, ok := reg.images[ref]
		if !ok {
			http.Error(w, "manifest unknown", http.StatusNotFound)
			return
		}
		digest = img.digest
		if img.index {
			// ghcr.io behaviour: an index is only returned when the client accepts it.
			if !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
				http.Error(w, "OCI index found, but accept header does not support OCI indexes", http.StatusNotFound)
				return
			}
			body = fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[`+
				`{"digest":"sha256:attestation","platform":{"os":"unknown","architecture":"unknown"}},`+
				`{"digest":"sha256:child-%s","platform":{"os":"linux","architecture":"amd64"}}]}`, ref)
		} else {
			body = fmt.Sprintf(`{"schemaVersion":2,"config":{"digest":"sha256:cfg-%s"}}`, ref)
		}
	}
	if !reg.noDigestHeader {
		w.Header().Set("Docker-Content-Digest", digest)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	fmt.Fprint(w, body) //nolint:errcheck
}

func toJSONArray(ss []string) string {
	if len(ss) == 0 {
		return "[]"
	}
	quoted := make([]string, len(ss))
	for i, s := range ss {
		quoted[i] = strconv.Quote(s)
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func imagesWithDigests(tags ...string) map[string]testImage {
	m := make(map[string]testImage, len(tags))
	for _, tag := range tags {
		m[tag] = testImage{digest: "sha256:" + tag, created: "2026-01-01T00:00:00Z"}
	}
	return m
}

// TestOCIWatcher_SelectsNewestTag covers tag ordering (C05-steps-06): a single
// moving tag, the highest semantic version, and the newest build for other tags.
func TestOCIWatcher_SelectsNewestTag(t *testing.T) {
	tests := []struct {
		name    string
		tags    []string
		images  map[string]testImage
		filter  string
		wantTag string
		wantErr string
	}{
		{
			name:    "single moving tag",
			tags:    []string{"latest"},
			images:  imagesWithDigests("latest"),
			wantTag: "latest",
		},
		{
			name:    "semver picks the highest version, not the lexical last",
			tags:    []string{"v1.10.0", "v1.2.0", "v1.9.0"},
			images:  imagesWithDigests("v1.10.0", "v1.2.0", "v1.9.0"),
			wantTag: "v1.10.0",
		},
		{
			name:    "semver release is above its prerelease",
			tags:    []string{"1.0.0", "1.0.0-rc.1", "0.9.0"},
			images:  imagesWithDigests("1.0.0", "1.0.0-rc.1", "0.9.0"),
			wantTag: "1.0.0",
		},
		{
			name: "sha tags use the newest build, not the lexical last",
			tags: []string{"sha-1def456", "sha-9abc123"},
			images: map[string]testImage{
				"sha-9abc123": {digest: "sha256:old", created: "2026-01-01T00:00:00Z"},
				"sha-1def456": {digest: "sha256:new", created: "2026-02-01T00:00:00Z"},
			},
			wantTag: "sha-1def456",
		},
		{
			name: "newest build reads the image behind a multi-arch index",
			tags: []string{"sha-aaa", "sha-bbb"},
			images: map[string]testImage{
				"sha-aaa": {digest: "sha256:index-aaa", created: "2026-03-01T00:00:00Z", index: true},
				"sha-bbb": {digest: "sha256:index-bbb", created: "2026-01-01T00:00:00Z", index: true},
			},
			wantTag: "sha-aaa",
		},
		{
			name: "tagFilter excludes non-matching tags",
			tags: []string{"latest", "sha-aaa", "sha-bbb"},
			images: map[string]testImage{
				"latest":  {digest: "sha256:latest", created: "2026-12-01T00:00:00Z"},
				"sha-aaa": {digest: "sha256:aaa", created: "2026-02-01T00:00:00Z"},
				"sha-bbb": {digest: "sha256:bbb", created: "2026-01-01T00:00:00Z"},
			},
			filter:  "^sha-",
			wantTag: "sha-aaa",
		},
		{
			name: "equal build times are an error",
			tags: []string{"sha-aaa", "sha-bbb"},
			images: map[string]testImage{
				"sha-aaa": {digest: "sha256:aaa", created: "1970-01-01T00:00:00Z"},
				"sha-bbb": {digest: "sha256:bbb", created: "1970-01-01T00:00:00Z"},
			},
			wantErr: "same build time",
		},
		{
			name:    "no matching tag is an error",
			tags:    []string{"v1.0.0", "v1.1.0"},
			images:  imagesWithDigests("v1.0.0", "v1.1.0"),
			filter:  "^sha-",
			wantErr: `matches tagFilter "^sha-" (2 tags listed)`,
		},
		{
			name:    "too many non-semver tags is an error",
			tags:    manyTags(51),
			images:  imagesWithDigests(manyTags(51)...),
			wantErr: "limited to 50 tags",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &testRegistry{name: "myorg/myapp", tags: tt.tags, images: tt.images}
			srv := reg.start(t)
			result, err := source.NewOCIWatcher(srv.URL+"/myorg/myapp", tt.filter).WithHTTPClient(srv.Client()).Watch(context.Background(), "sha256:prev")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantTag, result.Tag)
			assert.Equal(t, tt.images[tt.wantTag].digest, result.Digest)
			assert.True(t, result.Changed)
		})
	}
}

func manyTags(n int) []string {
	tags := make([]string, n)
	for i := range tags {
		tags[i] = fmt.Sprintf("build-%03d", i)
	}
	return tags
}

// TestOCIWatcher_AnonymousBearerToken covers the anonymous token flow that
// public ghcr.io and Docker Hub repositories require (C05-steps-07).
func TestOCIWatcher_AnonymousBearerToken(t *testing.T) {
	reg := &testRegistry{
		name: "myorg/myapp", auth: "bearer",
		tags:   []string{"sha-aaa", "sha-bbb"},
		images: map[string]testImage{"sha-aaa": {digest: "sha256:aaa", created: "2026-01-01T00:00:00Z"}, "sha-bbb": {digest: "sha256:bbb", created: "2026-02-01T00:00:00Z"}},
	}
	srv := reg.start(t)

	result, err := source.NewOCIWatcher(srv.URL+"/myorg/myapp", "").WithHTTPClient(srv.Client()).Watch(context.Background(), "sha256:aaa")
	require.NoError(t, err)
	assert.Equal(t, "sha-bbb", result.Tag)
	assert.True(t, result.Changed)

	reg.mu.Lock()
	defer reg.mu.Unlock()
	assert.Equal(t, 1, reg.tokenRequests, "one token per Watch call")
	assert.Equal(t, []string{""}, reg.tokenAuthz, "the token request must be anonymous")
	require.Len(t, reg.tokenQueries, 1)
	assert.Contains(t, reg.tokenQueries[0], "service=test-registry")
	assert.Contains(t, reg.tokenQueries[0], "scope=repository%3Amyorg%2Fmyapp%3Apull")
}

// TestOCIWatcher_CredentialsRequiredIsReported verifies that a registry that
// needs credentials gets a clear error and no token is ever leaked into it.
func TestOCIWatcher_CredentialsRequiredIsReported(t *testing.T) {
	reg := &testRegistry{name: "myorg/myapp", auth: "basic", tags: []string{"v1.0.0"}, images: imagesWithDigests("v1.0.0")}
	srv := reg.start(t)

	_, err := source.NewOCIWatcher(srv.URL+"/myorg/myapp", "").WithHTTPClient(srv.Client()).Watch(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires credentials")
	assert.Contains(t, err.Error(), "public repositories")
}

// TestOCIWatcher_FollowsPagination verifies that tags after the first
// tags/list page are seen (C05-steps-21).
func TestOCIWatcher_FollowsPagination(t *testing.T) {
	reg := &testRegistry{
		name: "myorg/myapp", pageSize: 1,
		tags:   []string{"v1.0.0", "v1.1.0", "v2.0.0"},
		images: imagesWithDigests("v1.0.0", "v1.1.0", "v2.0.0"),
	}
	srv := reg.start(t)

	result, err := source.NewOCIWatcher(srv.URL+"/myorg/myapp", "").WithHTTPClient(srv.Client()).Watch(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "v2.0.0", result.Tag)
}

// TestOCIWatcher_MultiArchIndexDigest verifies that a multi-arch tag resolves
// to its index digest, which needs the index media type in Accept (C05-steps-22).
func TestOCIWatcher_MultiArchIndexDigest(t *testing.T) {
	reg := &testRegistry{
		name: "myorg/myapp", tags: []string{"v1.0.0"},
		images: map[string]testImage{"v1.0.0": {digest: "sha256:index", index: true}},
	}
	srv := reg.start(t)

	result, err := source.NewOCIWatcher(srv.URL+"/myorg/myapp", "").WithHTTPClient(srv.Client()).Watch(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "sha256:index", result.Digest)
}

// TestOCIWatcher_DigestWithoutHeader verifies that the digest is computed from
// the manifest when the registry omits Docker-Content-Digest.
func TestOCIWatcher_DigestWithoutHeader(t *testing.T) {
	reg := &testRegistry{name: "myorg/myapp", noDigestHeader: true, tags: []string{"v1.0.0"}, images: imagesWithDigests("v1.0.0")}
	srv := reg.start(t)

	result, err := source.NewOCIWatcher(srv.URL+"/myorg/myapp", "").WithHTTPClient(srv.Client()).Watch(context.Background(), "")
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(`{"schemaVersion":2,"config":{"digest":"sha256:cfg-v1.0.0"}}`))
	assert.Equal(t, "sha256:"+hex.EncodeToString(sum[:]), result.Digest)
}

// TestOCIWatcher_NoChangeWhenDigestUnchanged verifies that Watch returns
// Changed=false when the newest tag digest matches lastDigest.
func TestOCIWatcher_NoChangeWhenDigestUnchanged(t *testing.T) {
	reg := &testRegistry{name: "myorg/myapp", tags: []string{"sha-aaa111"}, images: imagesWithDigests("sha-aaa111")}
	srv := reg.start(t)

	result, err := source.NewOCIWatcher(srv.URL+"/myorg/myapp", "").WithHTTPClient(srv.Client()).Watch(context.Background(), "sha256:sha-aaa111")
	require.NoError(t, err)
	assert.False(t, result.Changed)
	assert.Equal(t, "sha256:sha-aaa111", result.Digest)
}

// TestOCIWatcher_FirstRunNotChanged verifies that first-run (lastDigest="")
// does NOT return Changed=true but does return the digest to record.
func TestOCIWatcher_FirstRunNotChanged(t *testing.T) {
	reg := &testRegistry{name: "myorg/myapp", tags: []string{"latest"}, images: imagesWithDigests("latest")}
	srv := reg.start(t)

	result, err := source.NewOCIWatcher(srv.URL+"/myorg/myapp", "").WithHTTPClient(srv.Client()).Watch(context.Background(), "")
	require.NoError(t, err)
	assert.False(t, result.Changed, "First run must not return Changed=true")
	assert.Equal(t, "sha256:latest", result.Digest)
}

// TestOCIWatcher_InvalidTagFilterReturnsError verifies that an invalid regex is rejected.
func TestOCIWatcher_InvalidTagFilterReturnsError(t *testing.T) {
	reg := &testRegistry{name: "myorg/myapp", tags: []string{"v1.0.0"}, images: imagesWithDigests("v1.0.0")}
	srv := reg.start(t)

	_, err := source.NewOCIWatcher(srv.URL+"/myorg/myapp", "[invalid").WithHTTPClient(srv.Client()).Watch(context.Background(), "")
	assert.Error(t, err, "invalid regex must return error")
}

// TestOCIWatcher_EmptyRegistryReturnsError verifies validation.
func TestOCIWatcher_EmptyRegistryReturnsError(t *testing.T) {
	watcher := source.NewOCIWatcher("", "")
	_, err := watcher.Watch(context.Background(), "")
	assert.Error(t, err)
}

// TestWatchers_RefuseLoopback verifies that the watchers the controller builds
// apply the egress guard: a registry or git server on 127.0.0.1 (an httptest
// server) is refused with egress.ErrBlockedAddress before any request reaches
// it. Private ranges stay allowed; pkg/egress tests the address list.
func TestWatchers_RefuseLoopback(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := source.NewOCIWatcher(srv.URL+"/myorg/myapp", "").Watch(context.Background(), "")
	require.ErrorIs(t, err, egress.ErrBlockedAddress)
	assert.Contains(t, err.Error(), "is loopback")

	_, err = source.NewGitWatcher(srv.URL+"/repo.git", "main", "").Watch(context.Background(), "")
	require.ErrorIs(t, err, egress.ErrBlockedAddress)
	assert.Contains(t, err.Error(), "is loopback")

	assert.Zero(t, hits.Load(), "no request reaches the loopback server")
}
