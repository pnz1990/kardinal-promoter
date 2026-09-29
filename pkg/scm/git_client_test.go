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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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
