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

package scm_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestGoGitClient_PushCreatesBranch clones a local repo, commits and pushes a
// promotion branch, then checks the branch exists on the remote at the new
// commit. It guards against a push that reports success but sends nothing.
func TestGoGitClient_PushCreatesBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-git filesystem tests in short mode (may hang on macOS)")
	}
	ctx := context.Background()
	c := scm.NewGoGitClient()

	// Seed a remote with one commit on "main".
	seedDir := t.TempDir()
	_, err := gogit.PlainInitWithOptions(seedDir, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(seedDir, "README"), []byte("seed\n"), 0o600))
	require.NoError(t, c.CommitAll(ctx, seedDir, "seed", "t", "t@example.com"))

	remoteDir := t.TempDir()
	_, err = gogit.PlainClone(remoteDir, true, &gogit.CloneOptions{URL: seedDir})
	require.NoError(t, err)

	workDir := filepath.Join(t.TempDir(), "work")
	require.NoError(t, c.Clone(ctx, "file://"+remoteDir, "main", workDir, ""))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "README"), []byte("promoted\n"), 0o600))
	require.NoError(t, c.CommitAll(ctx, workDir, "promote", "t", "t@example.com"))

	work, err := gogit.PlainOpen(workDir)
	require.NoError(t, err)
	workHead, err := work.Head()
	require.NoError(t, err)

	require.NoError(t, c.Push(ctx, workDir, "origin", "kardinal/app-v1/test", "", false))

	remote, err := gogit.PlainOpen(remoteDir)
	require.NoError(t, err)
	ref, err := remote.Reference(plumbing.NewBranchReferenceName("kardinal/app-v1/test"), true)
	require.NoError(t, err, "pushed branch must exist on the remote")
	require.Equal(t, workHead.Hash(), ref.Hash())
}

// seedBareRemote creates a bare repository with one commit on main holding
// files, and returns its path.
func seedBareRemote(t *testing.T, files map[string]string) string {
	t.Helper()
	ctx := context.Background()
	c := scm.NewGoGitClient()
	seedDir := t.TempDir()
	_, err := gogit.PlainInitWithOptions(seedDir, &gogit.PlainInitOptions{
		InitOptions: gogit.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	require.NoError(t, err)
	for p, content := range files {
		full := filepath.Join(seedDir, p)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o750))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o600))
	}
	require.NoError(t, c.CommitAll(ctx, seedDir, "seed", "t", "t@example.com"))
	remoteDir := t.TempDir()
	_, err = gogit.PlainClone(remoteDir, true, &gogit.CloneOptions{URL: seedDir})
	require.NoError(t, err)
	return remoteDir
}

// TestGoGitClient_CloneSendsToken verifies Clone authenticates with the token
// (C05-steps-08, C06-scm-health-04) and trims a trailing newline from it
// (C06-scm-health-27). The error must not echo URL credentials.
func TestGoGitClient_CloneSendsToken(t *testing.T) {
	var mu sync.Mutex
	var gotAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := scm.NewGoGitClient()
	err := c.Clone(context.Background(), srv.URL+"/org/private.git", "main", filepath.Join(t.TempDir(), "w"), "ghp_TOKEN\n")
	require.Error(t, err)

	mu.Lock()
	first := append([]string(nil), gotAuth...)
	mu.Unlock()
	require.NotEmpty(t, first)
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_TOKEN"))
	assert.Equal(t, want, first[0], "clone must send the token as basic auth")

	// Credentials embedded in the URL never appear in the error.
	u := strings.Replace(srv.URL, "http://", "http://user:ghp_SECRET@", 1) + "/org/private.git"
	err = c.Clone(context.Background(), u, "main", filepath.Join(t.TempDir(), "w2"), "")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "ghp_SECRET")
}

// TestGoGitClient_HTTPErrorsEndWithTheReason covers the B47 family: go-git
// appends the HTTP response body to the error ("authentication required:
// Unauthorized\n"), so step messages ended with a newline, or with ": " when
// the body was empty. The error names the URL once and ends with the reason.
func TestGoGitClient_HTTPErrorsEndWithTheReason(t *testing.T) {
	serve := func(status int, body string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	ctx := context.Background()
	c := scm.NewGoGitClient()
	cases := []struct {
		name   string
		status int
		body   string
		run    func(t *testing.T, url string) error
		want   string // %s is the URL
	}{
		{name: "clone, body with a newline", status: http.StatusUnauthorized, body: "Unauthorized\n",
			run: func(t *testing.T, url string) error {
				return c.Clone(ctx, url, "main", filepath.Join(t.TempDir(), "w"), "tok")
			},
			want: "git clone %s: authentication required: Unauthorized"},
		{name: "clone, empty body", status: http.StatusUnauthorized,
			run: func(t *testing.T, url string) error {
				return c.Clone(ctx, url, "main", filepath.Join(t.TempDir(), "w"), "tok")
			},
			want: "git clone %s: authentication required"},
		{name: "clone at a commit", status: http.StatusForbidden, body: "Forbidden\r\n",
			run: func(t *testing.T, url string) error {
				return c.CloneAt(ctx, url, "abc123", filepath.Join(t.TempDir(), "w"), "tok")
			},
			want: "git clone %s: authorization failed: Forbidden"},
		{name: "push", status: http.StatusUnauthorized, body: "Unauthorized\n",
			run: func(t *testing.T, url string) error {
				work := filepath.Join(t.TempDir(), "w")
				require.NoError(t, c.Clone(ctx, "file://"+seedBareRemote(t, map[string]string{"a.txt": "a\n"}), "main", work, ""))
				repo, err := gogit.PlainOpen(work)
				require.NoError(t, err)
				require.NoError(t, repo.DeleteRemote("origin"))
				_, err = repo.CreateRemote(&gogitconfig.RemoteConfig{Name: "origin", URLs: []string{url}})
				require.NoError(t, err)
				return c.Push(ctx, work, "origin", "main", "tok", true)
			},
			want: "git push origin main: authentication required: Unauthorized"},
		// go-git reports other codes as "unexpected client error: unexpected
		// requesting "<url>/info/refs?service=git-upload-pack" status code: 500",
		// which named the URL a second time and dropped the body.
		{name: "clone, 500 with a body", status: http.StatusInternalServerError, body: "upstream\n  timed out\n",
			run: func(t *testing.T, url string) error {
				return c.Clone(ctx, url, "main", filepath.Join(t.TempDir(), "w"), "tok")
			},
			want: "git clone %s: HTTP 500 Internal Server Error: upstream timed out"},
		{name: "clone, 502 with an empty body", status: http.StatusBadGateway,
			run: func(t *testing.T, url string) error {
				return c.Clone(ctx, url, "main", filepath.Join(t.TempDir(), "w"), "tok")
			},
			want: "git clone %s: HTTP 502 Bad Gateway"},
		{name: "clone at a commit, 503", status: http.StatusServiceUnavailable, body: "maintenance\n",
			run: func(t *testing.T, url string) error {
				return c.CloneAt(ctx, url, "abc123", filepath.Join(t.TempDir(), "w"), "tok")
			},
			want: "git clone %s: HTTP 503 Service Unavailable: maintenance"},
		{name: "push, 500", status: http.StatusInternalServerError, body: "hook failed\n",
			run: func(t *testing.T, url string) error {
				work := filepath.Join(t.TempDir(), "w")
				require.NoError(t, c.Clone(ctx, "file://"+seedBareRemote(t, map[string]string{"a.txt": "a\n"}), "main", work, ""))
				repo, err := gogit.PlainOpen(work)
				require.NoError(t, err)
				require.NoError(t, repo.DeleteRemote("origin"))
				_, err = repo.CreateRemote(&gogitconfig.RemoteConfig{Name: "origin", URLs: []string{url}})
				require.NoError(t, err)
				return c.Push(ctx, work, "origin", "main", "tok", true)
			},
			want: "git push origin main: HTTP 500 Internal Server Error: hook failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := serve(tc.status, tc.body).URL + "/org/repo.git"
			err := tc.run(t, url)
			require.Error(t, err)
			want := tc.want
			if strings.Contains(want, "%s") {
				want = fmt.Sprintf(want, url)
				assert.Equal(t, 1, strings.Count(err.Error(), url), "the URL is named once: %q", err)
			}
			assert.Equal(t, want, err.Error())
		})
	}
}

// TestGoGitClient_HTMLErrorBodyIsOneLine covers a proxy's HTML error page,
// for a 403 and for a code go-git reports as unexpected (502): the whole
// response body goes into the error, so the step message
// spanned many lines and ended in "</html>". The body's whitespace collapses
// to single spaces and it is cut at 200 characters with "…", so the error is
// one line that names the URL once and ends with the reason.
func TestGoGitClient_HTMLErrorBodyIsOneLine(t *testing.T) {
	page := "<!DOCTYPE html>\r\n<html>\n<head>\n\t<title>403 Forbidden – proxy</title>\n</head>\n<body>\n" +
		"  <h1>Forbidden</h1>\n  <p>You don't have permission to access this repository through the proxy.</p>\n" +
		"  <p>Ask your network administrator to allow git traffic to this host.</p>\n  <hr>\n  <address>proxy/2.4</address>\n" +
		"</body>\n</html>\n"
	collapsed := []rune(strings.Join(strings.Fields(page), " "))
	require.Greater(t, len(collapsed), 200, "the page must be longer than the cap")
	short := "<html>\n<body>\n  Forbidden\n</body>\n</html>\n"

	ctx := context.Background()
	c := scm.NewGoGitClient()
	cases := []struct {
		name, body, want string // want: %s is the URL
		status           int
	}{
		{name: "long page", body: page, status: http.StatusForbidden,
			want: "git clone %s: authorization failed: " + strings.TrimSpace(string(collapsed[:200])) + "…"},
		{name: "short page", body: short, status: http.StatusForbidden,
			want: "git clone %s: authorization failed: <html> <body> Forbidden </body> </html>"},
		{name: "long page, 502", body: page, status: http.StatusBadGateway,
			want: "git clone %s: HTTP 502 Bad Gateway: " + strings.TrimSpace(string(collapsed[:200])) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			url := srv.URL + "/org/repo.git"
			err := c.Clone(ctx, url, "main", filepath.Join(t.TempDir(), "w"), "tok")
			require.Error(t, err)
			assert.Equal(t, fmt.Sprintf(tc.want, url), err.Error())
			assert.NotContains(t, err.Error(), "\n")
			assert.Equal(t, 1, strings.Count(err.Error(), url), "the URL is named once: %q", err)
		})
	}
}

// TestGoGitClient_ErrorBodyCredentialsAreRemoved covers an HTTP error body
// that holds URLs with credentials, such as a proxy page that echoes the URLs
// it refused. The credentials are removed before the body is cut at 200
// characters: one URL is before the cut, and the cut runs through the other's
// credentials ("https://user:t"), so cutting first would keep part of them.
func TestGoGitClient_ErrorBodyCredentialsAreRemoved(t *testing.T) {
	lead := "Blocked by the proxy. Sign in at https://user:tok@host.example/login and retry, or use the mirror "
	lead += strings.Repeat(".", 185-len(lead)) + " " // the second URL starts at character 186
	body := lead + "https://user:tok@host.example/mirror instead.\n"
	require.Equal(t, "https://user:t", body[186:200], "the cut must run through the second URL's credentials")

	removed := strings.ReplaceAll(strings.TrimSpace(body), "user:tok@", "")
	for _, tc := range []struct {
		status int
		reason string
	}{
		{http.StatusForbidden, "authorization failed"},
		{http.StatusInternalServerError, "HTTP 500 Internal Server Error"},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(srv.Close)
			url := srv.URL + "/org/repo.git"
			err := scm.NewGoGitClient().Clone(context.Background(), url, "main", filepath.Join(t.TempDir(), "w"), "tok")
			require.Error(t, err)

			assert.Equal(t, fmt.Sprintf("git clone %s: %s: %s…", url, tc.reason, removed[:200]), err.Error())
			assert.Contains(t, err.Error(), "Sign in at https://host.example/login and retry")
			assert.True(t, strings.HasSuffix(err.Error(), " https://host.example/mi…"), err.Error())
			assert.NotContains(t, err.Error(), "user")
			assert.NotContains(t, err.Error(), "tok@")
		})
	}
}

// seedUncheckoutable creates a bare repository whose main commit holds a file
// with a 300-byte name, which no checkout can write (file names are at most
// 255 bytes), and returns its path and the commit.
func seedUncheckoutable(t *testing.T) (string, plumbing.Hash) {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, true)
	require.NoError(t, err)
	st := repo.Storer
	store := func(o interface {
		Encode(plumbing.EncodedObject) error
	}) plumbing.Hash {
		obj := st.NewEncodedObject()
		require.NoError(t, o.Encode(obj))
		h, err := st.SetEncodedObject(obj)
		require.NoError(t, err)
		return h
	}
	blob := st.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	w, err := blob.Writer()
	require.NoError(t, err)
	_, err = w.Write([]byte("x\n"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	blobHash, err := st.SetEncodedObject(blob)
	require.NoError(t, err)
	tree := store(&object.Tree{Entries: []object.TreeEntry{
		{Name: strings.Repeat("n", 300), Mode: filemode.Regular, Hash: blobHash}}})
	sig := object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}
	commit := store(&object.Commit{Author: sig, Committer: sig, Message: "seed", TreeHash: tree})
	main := plumbing.NewBranchReferenceName("main")
	require.NoError(t, st.SetReference(plumbing.NewHashReference(main, commit)))
	require.NoError(t, st.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, main)))
	return dir, commit
}

// TestGoGitClient_LocalErrorsNameTheRepository verifies the errors of the
// local steps of a clone and a commit name the repository URL once, without
// credentials, as "resolve commit <sha> in <url>" does: the git-clone and
// git-commit steps do not add it.
func TestGoGitClient_LocalErrorsNameTheRepository(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	const secretURL = "https://user:ghp_SECRET@example.com/org/repo.git"
	const shown = "https://example.com/org/repo.git"
	// underFile returns a directory path whose parent is a regular file, so
	// it cannot be created.
	underFile := func(t *testing.T) string {
		f := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(f, nil, 0o600))
		return filepath.Join(f, "w")
	}

	t.Run("clone, create clone dir", func(t *testing.T) {
		err := c.Clone(ctx, secretURL, "main", underFile(t), "")
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "create clone dir for "+shown+": "), err.Error())
		assert.Equal(t, 1, strings.Count(err.Error(), shown), err.Error())
		assert.NotContains(t, err.Error(), "ghp_SECRET")
	})
	t.Run("clone at a commit, create clone dir", func(t *testing.T) {
		err := c.CloneAt(ctx, secretURL, "abc123", underFile(t), "")
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "create clone dir for "+shown+": "), err.Error())
		assert.Equal(t, 1, strings.Count(err.Error(), shown), err.Error())
		assert.NotContains(t, err.Error(), "ghp_SECRET")
	})
	t.Run("clone at a commit, checkout", func(t *testing.T) {
		remote, commit := seedUncheckoutable(t)
		url := "file://" + remote
		err := c.CloneAt(ctx, url, commit.String(), filepath.Join(t.TempDir(), "w"), "")
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "checkout "+commit.String()+" in "+url+": "), err.Error())
		assert.Equal(t, 1, strings.Count(err.Error(), url), err.Error())
	})
	t.Run("commit, get worktree", func(t *testing.T) {
		dir := t.TempDir()
		repo, err := gogit.PlainInit(dir, true) // bare: no worktree
		require.NoError(t, err)
		_, err = repo.CreateRemote(&gogitconfig.RemoteConfig{Name: "origin", URLs: []string{secretURL}})
		require.NoError(t, err)

		err = c.CommitAll(ctx, dir, "m", "t", "t@example.com")
		require.Error(t, err)
		assert.True(t, strings.HasPrefix(err.Error(), "get worktree of "+shown+": "), err.Error())
		assert.Equal(t, 1, strings.Count(err.Error(), shown), err.Error())
		assert.NotContains(t, err.Error(), "ghp_SECRET")
	})
}

// TestHTTPAuthUsername verifies the push/clone username per provider (C06-scm-health-31).
func TestHTTPAuthUsername(t *testing.T) {
	cases := []struct {
		url, wantUser string
	}{
		{"https://github.com/o/r.git", "x-access-token"},
		{"https://bitbucket.org/ws/r.git", "x-token-auth"},
		{"https://gitlab.com/g/r.git", "oauth2"},
		{"https://alice@bitbucket.org/ws/r.git", "alice"},
		{"https://dev.azure.com/org/p/_git/r", "x-access-token"},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			assert.Equal(t, tc.wantUser, scm.HTTPAuthUsernameForTest(tc.url, "tok"))
		})
	}
	assert.Empty(t, scm.HTTPAuthUsernameForTest("https://github.com/o/r.git", " \n"), "blank token sends no auth")
	assert.Empty(t, scm.HTTPAuthUsernameForTest("git@github.com:o/r.git", "tok"), "ssh remotes get no basic auth")
}

// TestGoGitClient_CommitAllNothingToCommit verifies a clean tree yields
// ErrNothingToCommit and no empty commit (E2E-04).
func TestGoGitClient_CommitAllNothingToCommit(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{"a.txt": "a\n"})
	work := filepath.Join(t.TempDir(), "w")
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, ""))

	repo, err := gogit.PlainOpen(work)
	require.NoError(t, err)
	before, err := repo.Head()
	require.NoError(t, err)

	err = c.CommitAll(ctx, work, "noop", "t", "t@example.com")
	require.ErrorIs(t, err, scm.ErrNothingToCommit)

	after, err := repo.Head()
	require.NoError(t, err)
	assert.Equal(t, before.Hash(), after.Hash(), "no empty commit may be created")
}

// TestGoGitClient_CommitAllStagesDeletions verifies deleted files are committed.
func TestGoGitClient_CommitAllStagesDeletions(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	work := filepath.Join(t.TempDir(), "w")
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, ""))
	require.NoError(t, os.Remove(filepath.Join(work, "b.txt")))
	require.NoError(t, c.CommitAll(ctx, work, "rm b", "t", "t@example.com"))

	repo, err := gogit.PlainOpen(work)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	st, err := wt.Status()
	require.NoError(t, err)
	assert.True(t, st.IsClean(), "deletion must be committed: %s", st.String())
}

// TestGoGitClient_PushNonFastForward verifies a stale push to a shared branch
// returns ErrNonFastForward (C06-scm-health-13, C06-scm-health-32), and that
// force=true overwrites a kardinal-owned branch from a fresh shallow clone
// (C05-steps-12).
func TestGoGitClient_PushNonFastForward(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{"a.txt": "a\n"})

	cloneAndCommit := func(name, content string) string {
		work := filepath.Join(t.TempDir(), name)
		require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, ""))
		require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte(content), 0o600))
		require.NoError(t, c.CommitAll(ctx, work, name, "t", "t@example.com"))
		return work
	}
	w1 := cloneAndCommit("w1", "one\n")
	w2 := cloneAndCommit("w2", "two\n")

	// Base branch: the first writer wins, the second gets ErrNonFastForward.
	require.NoError(t, c.Push(ctx, w1, "origin", "main", "", false))
	err := c.Push(ctx, w2, "origin", "main", "", false)
	require.ErrorIs(t, err, scm.ErrNonFastForward)

	// Promotion branch: a re-run from a fresh clone overwrites it with force.
	require.NoError(t, c.Push(ctx, w1, "origin", "kardinal/b/prod", "", false))
	require.ErrorIs(t, c.Push(ctx, w2, "origin", "kardinal/b/prod", "", false), scm.ErrNonFastForward)
	require.NoError(t, c.Push(ctx, w2, "origin", "kardinal/b/prod", "", true))

	bare, err := gogit.PlainOpen(remote)
	require.NoError(t, err)
	ref, err := bare.Reference(plumbing.NewBranchReferenceName("kardinal/b/prod"), true)
	require.NoError(t, err)
	w2repo, err := gogit.PlainOpen(w2)
	require.NoError(t, err)
	w2head, err := w2repo.Head()
	require.NoError(t, err)
	assert.Equal(t, w2head.Hash(), ref.Hash())
}

// TestGoGitClient_CloneAt verifies a specific commit is checked out.
func TestGoGitClient_CloneAt(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{"a.txt": "v1\n"})
	work := filepath.Join(t.TempDir(), "w")
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, ""))
	repo, err := gogit.PlainOpen(work)
	require.NoError(t, err)
	first, err := repo.Head()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte("v2\n"), 0o600))
	require.NoError(t, c.CommitAll(ctx, work, "v2", "t", "t@example.com"))
	require.NoError(t, c.Push(ctx, work, "origin", "main", "", false))

	at := filepath.Join(t.TempDir(), "at")
	require.NoError(t, c.CloneAt(ctx, "file://"+remote, first.Hash().String(), at, ""))
	b, err := os.ReadFile(filepath.Join(at, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v1\n", string(b))
}
