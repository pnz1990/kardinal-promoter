// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestGoGitClient_PushLeased (#2095): a leased push of kardinal's PR branch
// overwrites only the commit it leased. Leased on the branch's head, it
// forces; leased on another commit, or with the branch absent, it is
// ErrLeaseBroken and the branch keeps its head; an empty lease creates the
// branch and never overwrites one that exists with other commits.
func TestGoGitClient_PushLeased(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{"a.txt": "a\n"})
	const branch = "kardinal/b/prod"
	commit := func(name string) (string, string) {
		work := filepath.Join(t.TempDir(), name)
		require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, scm.TokenAuth("")))
		require.NoError(t, os.WriteFile(filepath.Join(work, "a.txt"), []byte(name+"\n"), 0o600))
		require.NoError(t, c.CommitAll(ctx, work, name, "t", "t@example.com"))
		r, err := gogit.PlainOpen(work)
		require.NoError(t, err)
		h, err := r.Head()
		require.NoError(t, err)
		return work, h.Hash().String()
	}
	head := func() string {
		bare, err := gogit.PlainOpen(remote)
		require.NoError(t, err)
		ref, err := bare.Reference(plumbing.NewBranchReferenceName(branch), true)
		if err != nil {
			return ""
		}
		return ref.Hash().String()
	}
	w1, c1 := commit("one")
	w2, c2 := commit("two")
	foreign, cf := commit("foreign")

	require.ErrorIs(t, c.PushLeased(ctx, w1, "origin", branch, scm.TokenAuth(""), c2), scm.ErrLeaseBroken,
		"leased on a commit while the branch is absent")
	assert.Empty(t, head())
	require.NoError(t, c.PushLeased(ctx, w1, "origin", branch, scm.TokenAuth(""), ""), "an empty lease creates it")
	assert.Equal(t, c1, head())
	require.NoError(t, c.PushLeased(ctx, w2, "origin", branch, scm.TokenAuth(""), c1), "leased on its head: forced")
	assert.Equal(t, c2, head())

	// Someone else pushes: no lease on an older head overwrites them.
	require.NoError(t, c.Push(ctx, foreign, "origin", branch, scm.TokenAuth(""), true))
	require.ErrorIs(t, c.PushLeased(ctx, w1, "origin", branch, scm.TokenAuth(""), c2), scm.ErrLeaseBroken)
	require.ErrorIs(t, c.PushLeased(ctx, w1, "origin", branch, scm.TokenAuth(""), ""), scm.ErrLeaseBroken)
	assert.Equal(t, cf, head(), "the foreign commit is kept")
}
