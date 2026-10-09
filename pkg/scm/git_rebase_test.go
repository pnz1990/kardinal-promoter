// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
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
		require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, ""))
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

	require.NoError(t, c.Push(ctx, first, "origin", "main", "", false))
	require.ErrorIs(t, c.Push(ctx, second, "origin", "main", "", false), scm.ErrNonFastForward)

	changed, err := c.RebaseOnRemote(ctx, second, "origin", "main", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"environments/b/kustomization.yaml", "environments/b/new/extra.yaml", "environments/b/old.yaml"}, changed)
	require.NoError(t, c.Push(ctx, second, "origin", "main", "", false))

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

	_, err = c.RebaseOnRemote(ctx, conflicting, "origin", "main", "")
	require.ErrorIs(t, err, scm.ErrRebaseConflict)
	assert.Contains(t, err.Error(), "environments/a/kustomization.yaml")
	require.ErrorIs(t, c.Push(ctx, conflicting, "origin", "main", "", false), scm.ErrNonFastForward,
		"the refused commit is left as it was")
}

// TestGoGitClient_RebaseOnRemote_NotMoved (#1504 QA): when the remote branch
// is still at the commit the change was made on, there is nothing to rebase
// onto: RebaseOnRemote says ErrBranchNotMoved and leaves the commit as it is,
// so a push refused for another reason is not taken for contention. After
// a rebase, the commit's new parent is the reference.
func TestGoGitClient_RebaseOnRemote_NotMoved(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{"environments/a/kustomization.yaml": "a: 1\n"})
	work := filepath.Join(t.TempDir(), "w")
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, ""))
	writeFiles(t, work, map[string]string{"environments/a/kustomization.yaml": "a: 2\n"})
	require.NoError(t, c.CommitAll(ctx, work, "promote a", "kardinal", "k@example.com"))
	before, err := c.HeadCommit(ctx, work)
	require.NoError(t, err)

	_, err = c.RebaseOnRemote(ctx, work, "origin", "main", "")
	require.ErrorIs(t, err, scm.ErrBranchNotMoved)
	after, err := c.HeadCommit(ctx, work)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the commit is left as it was")

	// Another writer moves the branch: a rebase, then not moved again.
	other := filepath.Join(t.TempDir(), "o")
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", other, ""))
	writeFiles(t, other, map[string]string{"README.md": "x\n"})
	require.NoError(t, c.CommitAll(ctx, other, "other", "o", "o@example.com"))
	require.NoError(t, c.Push(ctx, other, "origin", "main", "", false))
	_, err = c.RebaseOnRemote(ctx, work, "origin", "main", "")
	require.NoError(t, err)
	_, err = c.RebaseOnRemote(ctx, work, "origin", "main", "")
	require.ErrorIs(t, err, scm.ErrBranchNotMoved, "rebased onto the head already")
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
			if errs[i] = c.Clone(ctx, "file://"+remote, "main", work, ""); errs[i] != nil {
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
				err := c.Push(ctx, work, "origin", "main", "", false)
				if err == nil {
					return
				}
				if !errors.Is(err, scm.ErrNonFastForward) {
					errs[i] = err
					return
				}
				if _, err := c.RebaseOnRemote(ctx, work, "origin", "main", ""); err != nil {
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
	require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, ""))
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

	_, err = c.RebaseOnRemote(ctx, work, "origin", "main", "")
	require.ErrorIs(t, err, scm.ErrRebaseBaseMissing)
	assert.Contains(t, err.Error(), "1111111111111111111111111111111111111111")
}
