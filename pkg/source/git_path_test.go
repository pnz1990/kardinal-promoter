// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package source_test

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

// gitBackend is a work tree pushing to a bare repository that git
// http-backend serves, optionally behind basic auth.
type gitBackend struct {
	work string
	url  string
	srv  *httptest.Server
}

func newGitBackend(t *testing.T, user, pass string) *gitBackend {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	require.NoError(t, os.MkdirAll(work, 0o755))
	runGit(t, work, "init", "-q", "-b", "main")
	b := &gitBackend{work: work}
	b.commit(t, map[string]string{"README.md": "hello"}, "initial")
	bare := filepath.Join(root, "repo.git")
	runGit(t, root, "clone", "-q", "--bare", work, bare)
	runGit(t, bare, "config", "uploadpack.allowFilter", "true")
	runGit(t, work, "remote", "add", "origin", bare)

	backend := filepath.Join(runGit(t, root, "--exec-path"), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend is not installed")
	}
	h := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user != "" {
			if u, p, ok := r.BasicAuth(); !ok || u != user || p != pass {
				w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(b.srv.Close)
	b.url = b.srv.URL + "/repo.git"
	return b
}

// commit writes files, commits and pushes (when origin exists); it returns the SHA.
func (b *gitBackend) commit(t *testing.T, files map[string]string, msg string) string {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(b.work, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
	runGit(t, b.work, "add", "-A")
	runGit(t, b.work, "commit", "-q", "-m", msg)
	if runGit(t, b.work, "remote") != "" {
		runGit(t, b.work, "push", "-q", "origin", "main")
	}
	return runGit(t, b.work, "rev-parse", "HEAD")
}

func (b *gitBackend) watcher(glob string) *source.GitWatcher {
	return source.NewGitWatcher(b.url, "main", glob).WithHTTPClient(b.srv.Client())
}

// poll runs one Watch the way the reconciler does: lastDigest and
// LastRevision come from the previous result.
func poll(t *testing.T, w *source.GitWatcher, prev *source.WatchResult) *source.WatchResult {
	t.Helper()
	last := ""
	if prev != nil {
		last, w.LastRevision = prev.Digest, prev.Revision
	}
	res, err := w.Watch(context.Background(), last)
	require.NoError(t, err)
	return res
}

// TestGitWatcher_PathGlob covers spec.git.pathGlob against a real repository:
// the first poll records the newest matching commit; commits outside the glob
// change nothing; a commit inside it (also under other commits) is reported;
// the walk stops at the previous head and at discoveryLimit.
func TestGitWatcher_PathGlob(t *testing.T) {
	b := newGitBackend(t, "", "")
	base := b.commit(t, map[string]string{"apps/web/deploy.yaml": "v1"}, "web v1")
	w := b.watcher("apps/web/**")

	r := poll(t, w, nil)
	assert.Equal(t, base, r.Digest, "the first poll records the newest matching commit")
	assert.False(t, r.Changed)
	head := runGit(t, b.work, "rev-parse", "HEAD")
	assert.Equal(t, head, r.Revision)

	b.commit(t, map[string]string{"apps/api/deploy.yaml": "v1"}, "api only")
	r = poll(t, w, r)
	assert.False(t, r.Changed, "a commit outside the glob creates nothing")
	assert.Equal(t, base, r.Digest)
	assert.Equal(t, runGit(t, b.work, "rev-parse", "HEAD"), r.Revision)

	web := b.commit(t, map[string]string{"apps/web/deploy.yaml": "v2"}, "web v2")
	b.commit(t, map[string]string{"docs/notes.md": "x"}, "docs")
	r = poll(t, w, r)
	assert.True(t, r.Changed)
	assert.Equal(t, web, r.Digest, "the newest matching commit, under a later one")
	assert.Equal(t, web[:7], r.Tag)

	again := poll(t, w, r)
	assert.False(t, again.Changed, "an unchanged head reads nothing")
	assert.Equal(t, web, again.Digest)

	// A rename out of the glob counts (the old path matches).
	gone := b.commit(t, map[string]string{"docs/x": "y"}, "noop")
	_ = gone
	runGit(t, b.work, "mv", "apps/web/deploy.yaml", "moved.yaml")
	runGit(t, b.work, "commit", "-q", "-m", "move")
	runGit(t, b.work, "push", "-q", "origin", "main")
	moved := runGit(t, b.work, "rev-parse", "HEAD")
	r = poll(t, w, again)
	assert.True(t, r.Changed)
	assert.Equal(t, moved, r.Digest)

	// discoveryLimit: a matching commit more than the limit back is not seen.
	limited := b.watcher("apps/web/**")
	limited.DiscoveryLimit = 2
	b.commit(t, map[string]string{"apps/web/new.yaml": "1"}, "web new")
	b.commit(t, map[string]string{"a.txt": "1"}, "a")
	b.commit(t, map[string]string{"b.txt": "1"}, "b")
	res, err := limited.Watch(context.Background(), moved)
	require.NoError(t, err)
	assert.False(t, res.Changed, "the matching commit is 3 back, the limit is 2")
	assert.Equal(t, moved, res.Digest)

	for _, glob := range []string{"*.txt", "{a,b}.txt"} {
		res, err := b.watcher(glob).Watch(context.Background(), "")
		require.NoError(t, err, glob)
		assert.Equal(t, runGit(t, b.work, "rev-parse", "HEAD"), res.Digest, glob)
	}

	_, err = b.watcher("[").Watch(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `pathGlob "[" is not a valid glob`)
}

// TestGitWatcher_PathGlobFirstPollWithoutMatch records the head as the
// baseline when no commit in reach matches, so the next matching commit is a
// change.
func TestGitWatcher_PathGlobFirstPollWithoutMatch(t *testing.T) {
	b := newGitBackend(t, "", "")
	w := b.watcher("charts/**")
	r := poll(t, w, nil)
	head := runGit(t, b.work, "rev-parse", "HEAD")
	assert.Equal(t, head, r.Digest)
	assert.False(t, r.Changed)

	sha := b.commit(t, map[string]string{"charts/app/Chart.yaml": "version: 1.0.0"}, "chart")
	r = poll(t, w, r)
	assert.True(t, r.Changed)
	assert.Equal(t, sha, r.Digest)
}

// TestGitWatcher_HTTPCredentials covers a private repository over HTTP(S):
// without credentials the error asks for secretRef; a token is sent as the
// password of user git (or the Secret's username); a wrong token is
// reported without echoing it. pathGlob fetches use the same credentials.
func TestGitWatcher_HTTPCredentials(t *testing.T) {
	const token = "tok-123456"
	b := newGitBackend(t, "git", token)
	head := runGit(t, b.work, "rev-parse", "HEAD")

	_, err := b.watcher("").Watch(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication required")
	assert.Contains(t, err.Error(), "set secretRef")

	w := b.watcher("")
	w.Credentials = source.Credentials{Token: token}
	r, err := w.Watch(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, head, r.Digest)

	w = b.watcher("**/*.md")
	w.Credentials = source.Credentials{Username: "git", Password: token}
	r, err = w.Watch(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, head, r.Digest, "pathGlob fetch authenticates too")

	w = b.watcher("")
	w.Credentials = source.Credentials{Token: "wrong-secret-token"}
	_, err = w.Watch(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "with the credentials from secretRef")
	assert.NotContains(t, err.Error(), "wrong-secret-token")
}
