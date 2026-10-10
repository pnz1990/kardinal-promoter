// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenRepo (#1646): openRepo opens a clone's working tree like
// gogit.PlainOpen (or a bare repository), with its packfiles kept open
// until close, and refuses a directory that is neither.
func TestOpenRepo(t *testing.T) {
	ctx := context.Background()
	c := NewGoGitClient()
	src := t.TempDir()
	_, err := gogit.PlainInit(src, false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("a\n"), 0o600))
	require.NoError(t, c.CommitAll(ctx, src, "seed", "t", "t@example.com"))
	dir := filepath.Join(t.TempDir(), "clone")
	_, err = gogit.PlainClone(dir, false, &gogit.CloneOptions{URL: src})
	require.NoError(t, err)

	repo, closeRepo, err := openRepo(dir)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	f, err := tree.File("a.txt")
	require.NoError(t, err)
	got, err := f.Contents()
	require.NoError(t, err)
	assert.Equal(t, "a\n", got)
	require.NoError(t, closeRepo())

	bare := t.TempDir()
	_, err = gogit.PlainInit(bare, true)
	require.NoError(t, err)
	repo, closeRepo, err = openRepo(bare)
	require.NoError(t, err, "a bare repository opens too")
	_, err = repo.Worktree()
	require.ErrorIs(t, err, gogit.ErrIsBareRepository)
	require.NoError(t, closeRepo())

	_, _, err = openRepo(t.TempDir())
	require.ErrorIs(t, err, gogit.ErrRepositoryNotExists)
}
