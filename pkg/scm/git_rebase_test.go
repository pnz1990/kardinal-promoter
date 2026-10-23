// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// remoteFiles returns path → content of the remote branch head, and the
// number of commits on it.
func remoteFiles(t *testing.T, remote, branch string) (map[string]string, int) {
	t.Helper()
	repo, err := gogit.PlainOpen(remote)
	require.NoError(t, err)
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	require.NoError(t, err)
	head, err := repo.CommitObject(ref.Hash())
	require.NoError(t, err)
	tree, err := head.Tree()
	require.NoError(t, err)
	files := map[string]string{}
	require.NoError(t, tree.Files().ForEach(func(f *object.File) error {
		c, err := f.Contents()
		files[f.Name] = c
		return err
	}))
	n := 0
	require.NoError(t, object.NewCommitPreorderIter(head, nil, nil).ForEach(func(*object.Commit) error { n++; return nil }))
	return files, n
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if c == "" {
			require.NoError(t, os.Remove(full))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(c), 0o600))
	}
}

// TestGoGitClient_RebaseOnRemote: a writer whose push was refused because
// another writer pushed first moves its commit onto the new head: the other
// writer's files and its own are both on the branch (no lost update), its
// message is kept, and deletions and new directories are replayed. A commit
// that changes a file the other writer changed is refused with
// ErrRebaseConflict and left as it was.
func TestGoGitClient_RebaseOnRemote(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{
		"environments/a/kustomization.yaml": "a: 1\n",
		"environments/b/kustomization.yaml": "b: 1\n",
		"environments/b/old.yaml":           "old\n",
		"README.md":                         "readme\n",
	})
	clone := func(name string, files map[string]string) string {
		work := filepath.Join(t.TempDir(), name)
		require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, scm.GitAuth{}))
		writeFiles(t, work, files)
		require.NoError(t, c.CommitAll(ctx, work, "promote "+name, "kardinal", "k@example.com"))
		return work
	}
	first := clone("a", map[string]string{"environments/a/kustomization.yaml": "a: 2\n"})
	second := clone("b", map[string]string{
		"environments/b/kustomization.yaml": "b: 2\n",
		"environments/b/old.yaml":           "",
		"environments/b/new/extra.yaml":     "extra\n",
	})
	conflicting := clone("a2", map[string]string{"environments/a/kustomization.yaml": "a: 3\n"})

	require.NoError(t, c.Push(ctx, first, "origin", "main", scm.GitAuth{}, false))
	require.ErrorIs(t, c.Push(ctx, second, "origin", "main", scm.GitAuth{}, false), scm.ErrNonFastForward)

	// Each rebase is a git operation of its own, counted by result (#2366).
	rebases := func(result string) float64 {
		return testutil.ToFloat64(scm.GitOperationsTotal.WithLabelValues("rebase", result))
	}
	ok0, conflict0 := rebases("ok"), rebases("conflict")
	changed, err := c.RebaseOnRemote(ctx, second, "origin", "main", scm.GitAuth{})
	require.NoError(t, err)
	assert.Equal(t, ok0+1, rebases("ok"), "a rebase that landed")
	assert.Equal(t, []string{"environments/b/kustomization.yaml", "environments/b/new/extra.yaml", "environments/b/old.yaml"}, changed)
	require.NoError(t, c.Push(ctx, second, "origin", "main", scm.GitAuth{}, false))

	files, commits := remoteFiles(t, remote, "main")
	assert.Equal(t, map[string]string{
		"environments/a/kustomization.yaml": "a: 2\n",
		"environments/b/kustomization.yaml": "b: 2\n",
		"environments/b/new/extra.yaml":     "extra\n",
		"README.md":                         "readme\n",
	}, files)
	assert.Equal(t, 3, commits, "seed, a, b: linear")
	head, err := c.HeadCommit(ctx, second)
	require.NoError(t, err)
	repo, err := gogit.PlainOpen(second)
	require.NoError(t, err)
	hc, err := repo.CommitObject(plumbing.NewHash(head))
	require.NoError(t, err)
	assert.Equal(t, "promote b", hc.Message)
	assert.Equal(t, "kardinal", hc.Author.Name)

	_, err = c.RebaseOnRemote(ctx, conflicting, "origin", "main", scm.GitAuth{})
	require.ErrorIs(t, err, scm.ErrRebaseConflict)
	assert.Equal(t, conflict0+1, rebases("conflict"), "a rebase refused by the other writer's change")
	assert.Contains(t, err.Error(), "environments/a/kustomization.yaml")
	require.ErrorIs(t, c.Push(ctx, conflicting, "origin", "main", scm.GitAuth{}, false), scm.ErrNonFastForward,
		"the refused commit is left as it was")
}

// TestGoGitClient_RebaseOnRemote_NotMoved (#1504 QA): when the remote branch
// is still at the commit the change was made on, there is nothing to rebase
// onto: RebaseOnRemote says ErrBranchNotMoved and leaves the commit as it is,
// so a push refused for another reason is not taken for contention, once
// it has fetched again for the wait (#2351). After a rebase, the commit's
// new parent is the reference.
func TestGoGitClient_RebaseOnRemote_NotMoved(t *testing.T) {
	const wait = 200 * time.Millisecond
	scm.SetNotMovedWaitForTest(t, wait)
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{"environments/a/kustomization.yaml": "a: 1\n"})
	work := filepath.Join(t.TempDir(), "w")
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, scm.GitAuth{}))
	writeFiles(t, work, map[string]string{"environments/a/kustomization.yaml": "a: 2\n"})
	require.NoError(t, c.CommitAll(ctx, work, "promote a", "kardinal", "k@example.com"))
	before, err := c.HeadCommit(ctx, work)
	require.NoError(t, err)

	start := time.Now()
	_, err = c.RebaseOnRemote(ctx, work, "origin", "main", scm.GitAuth{})
	took := time.Since(start)
	require.ErrorIs(t, err, scm.ErrBranchNotMoved)
	assert.GreaterOrEqual(t, took, wait, "fetched again for the wait first")
	// The wait, then at most a fetch or two: not ten waits.
	assert.Less(t, took, wait+1500*time.Millisecond, "the wait is bounded by notMovedWait")
	after, err := c.HeadCommit(ctx, work)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the commit is left as it was")

	// Another writer moves the branch: a rebase, then not moved again.
	other := filepath.Join(t.TempDir(), "o")
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", other, scm.GitAuth{}))
	writeFiles(t, other, map[string]string{"README.md": "x\n"})
	require.NoError(t, c.CommitAll(ctx, other, "other", "o", "o@example.com"))
	require.NoError(t, c.Push(ctx, other, "origin", "main", scm.GitAuth{}, false))
	_, err = c.RebaseOnRemote(ctx, work, "origin", "main", scm.GitAuth{})
	require.NoError(t, err)
	_, err = c.RebaseOnRemote(ctx, work, "origin", "main", scm.GitAuth{})
	require.ErrorIs(t, err, scm.ErrBranchNotMoved, "rebased onto the head already")
}

// TestGoGitClient_RebaseOnRemote_UpdateInFlight (#2351): a push refused
// while another writer's update held the branch's lock ("cannot lock ref")
// can be followed by a fetch that still sees the old head. RebaseOnRemote
// fetches again until the branch moves (here 300ms later, while it waits)
// and rebases onto it; a canceled context ends the wait.
//
// Covers SCM-GIT-REBASE-INFLIGHT-01.
func TestGoGitClient_RebaseOnRemote_UpdateInFlight(t *testing.T) {
	// The other writer's push lands at 300ms; a slow runner can take longer
	// than the 2s default to apply it (a push delayed to 2.1s failed). The
	// wait only has to outlast the push; the cancellation below ends it.
	scm.SetNotMovedWaitForTest(t, 30*time.Second)
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{"environments/a/kustomization.yaml": "a: 1\n", "README.md": "r\n"})
	work := filepath.Join(t.TempDir(), "w")
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, scm.GitAuth{}))
	writeFiles(t, work, map[string]string{"environments/a/kustomization.yaml": "a: 2\n"})
	require.NoError(t, c.CommitAll(ctx, work, "promote a", "kardinal", "k@example.com"))
	other := filepath.Join(t.TempDir(), "o")
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", other, scm.GitAuth{}))
	writeFiles(t, other, map[string]string{"README.md": "x\n"})
	require.NoError(t, c.CommitAll(ctx, other, "other", "o", "o@example.com"))
	theirs, err := c.HeadCommit(ctx, other)
	require.NoError(t, err)

	// The other writer's update lands while RebaseOnRemote waits.
	pushed := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		pushed <- c.Push(ctx, other, "origin", "main", scm.GitAuth{}, false)
	}()
	_, err = c.RebaseOnRemote(ctx, work, "origin", "main", scm.GitAuth{})
	require.NoError(t, <-pushed)
	require.NoError(t, err, "the branch moved within the wait")
	repo, err := gogit.PlainOpen(work)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	assert.Equal(t, []plumbing.Hash{plumbing.NewHash(theirs)}, commit.ParentHashes, "rebased onto the other writer's commit")
	require.NoError(t, c.Push(ctx, work, "origin", "main", scm.GitAuth{}, false))

	// A canceled context ends the wait at once.
	writeFiles(t, work, map[string]string{"environments/a/kustomization.yaml": "a: 3\n"})
	require.NoError(t, c.CommitAll(ctx, work, "promote a again", "kardinal", "k@example.com"))
	canceled, cancel := context.WithCancel(ctx)
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err = c.RebaseOnRemote(canceled, work, "origin", "main", scm.GitAuth{})
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), time.Second, "the wait ends with the context")
}

// TestGoGitClient_RebaseOnRemote_CanceledFetch: a context that ends during
// a fetch, not only during the wait between fetches, is the rebase's error
// (errors.Is context.Canceled): go-git reports it as text. Here it ended
// before the first fetch. TestGoGitClient_RebaseOnRemote_UpdateInFlight's
// cancel landed in a fetch on CI's race build once (release/v0.12, #2387).
func TestGoGitClient_RebaseOnRemote_CanceledFetch(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{"environments/a/kustomization.yaml": "a: 1\n"})
	work := filepath.Join(t.TempDir(), "w")
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, scm.GitAuth{}))
	writeFiles(t, work, map[string]string{"environments/a/kustomization.yaml": "a: 2\n"})
	require.NoError(t, c.CommitAll(ctx, work, "promote a", "kardinal", "k@example.com"))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := c.RebaseOnRemote(canceled, work, "origin", "main", scm.GitAuth{})
	require.ErrorIs(t, err, context.Canceled)
}

// TestGoGitClient_RebaseOnRemote_CanceledDuringFetch: a context that ends
// while a fetch is in flight is the rebase's error (errors.Is
// context.Canceled), never ErrRebaseBaseMissing: the remote is an HTTP
// server that answers no request, so each fetch is still waiting when the
// context ends. Both of RebaseOnRemote's fetches: the remote head's
// (git_rebase.go's fetch, whose go-git error was text: the UpdateInFlight
// failures on #2387 and #2391) and the missing base's (QA-2 on #2387).
func TestGoGitClient_RebaseOnRemote_CanceledDuringFetch(t *testing.T) {
	for _, baseMissing := range []bool{false, true} {
		t.Run(fmt.Sprintf("baseMissing=%v", baseMissing), func(t *testing.T) {
			ctx := context.Background()
			c := scm.NewGoGitClient()
			reached := make(chan struct{}, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				select {
				case reached <- struct{}{}:
				default:
				}
				<-r.Context().Done()
			}))
			t.Cleanup(srv.Close)
			remote := seedBareRemote(t, map[string]string{"README.md": "r\n"})
			work := filepath.Join(t.TempDir(), "w")
			require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, scm.GitAuth{}))
			writeFiles(t, work, map[string]string{"README.md": "w\n"})
			require.NoError(t, c.CommitAll(ctx, work, "promote", "kardinal", "k@example.com"))
			repo, err := gogit.PlainOpen(work)
			require.NoError(t, err)
			if baseMissing {
				head, err := repo.Head()
				require.NoError(t, err)
				cur, err := repo.CommitObject(head.Hash())
				require.NoError(t, err)
				orphan := &object.Commit{Author: cur.Author, Committer: cur.Committer, Message: "promote", TreeHash: cur.TreeHash,
					ParentHashes: []plumbing.Hash{plumbing.NewHash("1111111111111111111111111111111111111111")}}
				obj := repo.Storer.NewEncodedObject()
				require.NoError(t, orphan.Encode(obj))
				h, err := repo.Storer.SetEncodedObject(obj)
				require.NoError(t, err)
				require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(head.Name(), h)))
			}
			require.NoError(t, repo.DeleteRemote("origin"))
			_, err = repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{srv.URL + "/repo.git"}})
			require.NoError(t, err)

			canceled, cancel := context.WithCancel(ctx)
			defer cancel()
			go func() {
				<-reached
				cancel()
			}()
			_, err = c.RebaseOnRemote(canceled, work, "origin", "main", scm.GitAuth{})
			require.ErrorIs(t, err, context.Canceled)
			assert.NotErrorIs(t, err, scm.ErrRebaseBaseMissing)
		})
	}
}

// TestGoGitClient_PushRefusedWhileAnUpdateHoldsTheLock (#2351): the race
// TestGoGitClient_ConcurrentWritersLoseNothing lost once, made certain. The
// remote's refs/heads/main.lock stands for another writer's update in
// flight: git-receive-pack refuses this writer's push ("cannot lock ref
// ... File exists", a lost race), and the fetch after it still sees the
// branch where it was. The other update lands 300ms later; RebaseOnRemote,
// fetching again meanwhile, rebases onto it and the push lands, keeping
// both writers' changes. Before #2351 the rebase said "rebase onto main at
// <base>: the remote branch did not move", the message CI saw.
//
// Covers SCM-GIT-REBASE-INFLIGHT-01.
func TestGoGitClient_PushRefusedWhileAnUpdateHoldsTheLock(t *testing.T) {
	// The other update lands at 300ms; the wait only has to outlast it on a
	// slow runner (as in TestGoGitClient_RebaseOnRemote_UpdateInFlight).
	scm.SetNotMovedWaitForTest(t, 30*time.Second)
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{"environments/a/kustomization.yaml": "a: 1\n", "environments/b/kustomization.yaml": "b: 1\n"})
	mine, theirs := filepath.Join(t.TempDir(), "mine"), filepath.Join(t.TempDir(), "theirs")
	for dir, env := range map[string]string{mine: "a", theirs: "b"} {
		require.NoError(t, c.Clone(ctx, "file://"+remote, "main", dir, scm.GitAuth{}))
		writeFiles(t, dir, map[string]string{"environments/" + env + "/kustomization.yaml": env + ": 2\n"})
		require.NoError(t, c.CommitAll(ctx, dir, "promote "+env, "k", "k@example.com"))
	}
	lock := filepath.Join(remote, "refs", "heads", "main.lock")
	require.NoError(t, os.WriteFile(lock, nil, 0o600))

	_, before := remoteFiles(t, remote, "main")
	err := c.Push(ctx, mine, "origin", "main", scm.GitAuth{}, false)
	require.ErrorIs(t, err, scm.ErrNonFastForward, "a push refused while the lock is held is a lost race")
	// The status says "failed to update ref"; the sideband says why.
	assert.Contains(t, err.Error(), "main.lock': File exists", "the server's refusal is kept: %v", err)
	_, after := remoteFiles(t, remote, "main")
	require.Equal(t, before, after, "refused by the server's lock: the branch has not moved")

	landed := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := os.Remove(lock); err != nil {
			landed <- err
			return
		}
		landed <- c.Push(ctx, theirs, "origin", "main", scm.GitAuth{}, false)
	}()
	_, err = c.RebaseOnRemote(ctx, mine, "origin", "main", scm.GitAuth{})
	require.NoError(t, <-landed)
	require.NoError(t, err, "the other update landed within the wait")
	require.NoError(t, c.Push(ctx, mine, "origin", "main", scm.GitAuth{}, false))
	files, commits := remoteFiles(t, remote, "main")
	assert.Equal(t, "a: 2\n", files["environments/a/kustomization.yaml"])
	assert.Equal(t, "b: 2\n", files["environments/b/kustomization.yaml"])
	assert.Equal(t, 3, commits)
}

// TestGoGitClient_ConcurrentWritersLoseNothing: ten writers, each changing
// its own path, push to one branch at once and rebase until their push
// lands. Every change is on the branch and history is linear.
func TestGoGitClient_ConcurrentWritersLoseNothing(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	seed := map[string]string{"README.md": "readme\n"}
	for i := range 10 {
		seed[fmt.Sprintf("environments/p%d/kustomization.yaml", i)] = "v: 0\n"
	}
	remote := seedBareRemote(t, seed)
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			work := filepath.Join(t.TempDir(), fmt.Sprint(i))
			if errs[i] = c.Clone(ctx, "file://"+remote, "main", work, scm.GitAuth{}); errs[i] != nil {
				return
			}
			p := filepath.Join(work, fmt.Sprintf("environments/p%d/kustomization.yaml", i))
			if errs[i] = os.WriteFile(p, []byte("v: 1\n"), 0o600); errs[i] != nil {
				return
			}
			if errs[i] = c.CommitAll(ctx, work, fmt.Sprint("p", i), "k", "k@example.com"); errs[i] != nil {
				return
			}
			for attempt := 0; attempt < 50; attempt++ {
				err := c.Push(ctx, work, "origin", "main", scm.GitAuth{}, false)
				if err == nil {
					return
				}
				if !errors.Is(err, scm.ErrNonFastForward) {
					errs[i] = err
					return
				}
				if _, err := c.RebaseOnRemote(ctx, work, "origin", "main", scm.GitAuth{}); err != nil {
					errs[i] = err
					return
				}
			}
			errs[i] = fmt.Errorf("writer %d never landed", i)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "writer %d", i)
	}
	files, commits := remoteFiles(t, remote, "main")
	var got []string
	for p, v := range files {
		if v == "v: 1\n" {
			got = append(got, p)
		}
	}
	sort.Strings(got)
	assert.Len(t, got, 10, "every writer's change is on the branch: %v", files)
	assert.Equal(t, 11, commits)
}

// TestGoGitClient_RebaseOnRemote_BaseMissing (#1606): a HEAD whose parent is
// not in the clone is refused with ErrRebaseBaseMissing, naming both
// commits, so git-push redoes the change from a fresh clone instead of
// failing the same way on every retry in this work directory.
func TestGoGitClient_RebaseOnRemote_BaseMissing(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{"README.md": "r\n"})
	work := filepath.Join(t.TempDir(), "w")
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, scm.GitAuth{}))
	// HEAD made on a commit the clone does not have.
	repo, err := gogit.PlainOpen(work)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	cur, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	orphan := &object.Commit{Author: cur.Author, Committer: cur.Committer, Message: "promote", TreeHash: cur.TreeHash,
		ParentHashes: []plumbing.Hash{plumbing.NewHash("1111111111111111111111111111111111111111")}}
	obj := repo.Storer.NewEncodedObject()
	require.NoError(t, orphan.Encode(obj))
	h, err := repo.Storer.SetEncodedObject(obj)
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(head.Name(), h)))

	_, err = c.RebaseOnRemote(ctx, work, "origin", "main", scm.GitAuth{})
	require.ErrorIs(t, err, scm.ErrRebaseBaseMissing)
	assert.Contains(t, err.Error(), "1111111111111111111111111111111111111111")

	// A context that ends during the base's fetch is the cancellation, not
	// a missing base: git-push redoes the change from a fresh clone on
	// ErrRebaseBaseMissing (QA-2's probe on #2387).
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = c.RebaseOnRemote(canceled, work, "origin", "main", scm.GitAuth{})
	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, scm.ErrRebaseBaseMissing)
}

// TestGoGitClient_RebaseOnRemote_BaseFetched (#1606): a HEAD made on an
// earlier head of the branch that the shallow clone does not have is rebased
// all the same: RebaseOnRemote fetches the branch's recent history, finds the
// base there and replays the change onto the current head, keeping the other
// writers' commits.
func TestGoGitClient_RebaseOnRemote_BaseFetched(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{"README.md": "r\n", "envs/a.yaml": "a: 1\n"})
	push := func(name string, files map[string]string) string {
		w := filepath.Join(t.TempDir(), name)
		require.NoError(t, c.Clone(ctx, "file://"+remote, "main", w, scm.GitAuth{}))
		writeFiles(t, w, files)
		require.NoError(t, c.CommitAll(ctx, w, "write "+name, "other", "o@example.com"))
		require.NoError(t, c.Push(ctx, w, "origin", "main", scm.GitAuth{}, false))
		h, err := c.HeadCommit(ctx, w)
		require.NoError(t, err)
		return h
	}
	work := filepath.Join(t.TempDir(), "w")
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, scm.GitAuth{}))
	earlier := push("x", map[string]string{"envs/x.yaml": "x\n"})
	push("y", map[string]string{"envs/y.yaml": "y\n"})

	// HEAD: this promotion's change, made on the earlier head the clone
	// never fetched.
	repo, err := gogit.PlainOpen(work)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	cur, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	// Its tree is the earlier head's (x.yaml) with this change (a.yaml).
	writeFiles(t, work, map[string]string{"envs/a.yaml": "a: 2\n", "envs/x.yaml": "x\n"})
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.AddGlob("envs"))
	tmp, err := wt.Commit("tmp", &gogit.CommitOptions{Author: &cur.Author})
	require.NoError(t, err)
	tc, err := repo.CommitObject(tmp)
	require.NoError(t, err)
	promo := &object.Commit{Author: cur.Author, Committer: cur.Committer, Message: "promote a", TreeHash: tc.TreeHash,
		ParentHashes: []plumbing.Hash{plumbing.NewHash(earlier)}}
	obj := repo.Storer.NewEncodedObject()
	require.NoError(t, promo.Encode(obj))
	h, err := repo.Storer.SetEncodedObject(obj)
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(head.Name(), h)))
	_, err = repo.CommitObject(plumbing.NewHash(earlier))
	require.ErrorIs(t, err, plumbing.ErrObjectNotFound, "the clone lacks the base")

	changed, err := c.RebaseOnRemote(ctx, work, "origin", "main", scm.GitAuth{})
	require.NoError(t, err)
	assert.Equal(t, []string{"envs/a.yaml"}, changed)
	require.NoError(t, c.Push(ctx, work, "origin", "main", scm.GitAuth{}, false))
	files, _ := remoteFiles(t, remote, "main")
	assert.Equal(t, map[string]string{"README.md": "r\n", "envs/a.yaml": "a: 2\n", "envs/x.yaml": "x\n", "envs/y.yaml": "y\n"}, files)
}
