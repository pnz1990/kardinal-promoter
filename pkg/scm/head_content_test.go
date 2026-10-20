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

// TestGoGitClient_SameContentAsHead (#2095): the head of kardinal's PR
// branch holds what the step is about to push only when its commit has the
// same tree and the same parents as the local HEAD: another commit time,
// message or author does not matter, one file more or another parent does.
//
// Covers GIT-PUSH-FOREIGN-02.
func TestGoGitClient_SameContentAsHead(t *testing.T) {
	ctx := context.Background()
	c := scm.NewGoGitClient()
	remote := seedBareRemote(t, map[string]string{"a.txt": "a\n"})
	const branch = "kardinal/b/prod"
	// commit clones main and commits each of files in turn, one commit per
	// map, with message msg.
	commit := func(name, msg string, files ...map[string]string) (string, string) {
		work := filepath.Join(t.TempDir(), name)
		require.NoError(t, c.Clone(ctx, "file://"+remote, "main", work, scm.TokenAuth("")))
		for _, fs := range files {
			for f, content := range fs {
				require.NoError(t, os.WriteFile(filepath.Join(work, f), []byte(content), 0o600))
			}
			require.NoError(t, c.CommitAll(ctx, work, msg, "t", "t@example.com"))
		}
		r, err := gogit.PlainOpen(work)
		require.NoError(t, err)
		h, err := r.Head()
		require.NoError(t, err)
		return work, h.Hash().String()
	}
	ours, _ := commit("ours", "kardinal: promote", map[string]string{"a.txt": "v2\n"})
	for _, tc := range []struct {
		name  string
		files []map[string]string
		want  bool
	}{
		{name: "same tree and parent, another message and time", files: []map[string]string{{"a.txt": "v2\n"}}, want: true},
		{name: "same tree, another parent", files: []map[string]string{{"a.txt": "other\n"}, {"a.txt": "v2\n"}}},
		{name: "same parent, one file differs", files: []map[string]string{{"a.txt": "v2\n", "b.txt": "extra\n"}}},
		{name: "same parent, another file content", files: []map[string]string{{"a.txt": "v3\n"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			theirs, sha := commit("theirs", "someone else's", tc.files...)
			require.NoError(t, c.Push(ctx, theirs, "origin", branch, scm.TokenAuth(""), true))
			same, err := c.SameContentAsHead(ctx, ours, "origin", branch, scm.TokenAuth(""), sha)
			require.NoError(t, err)
			assert.Equal(t, tc.want, same)
		})
	}

	// The head moved since it was read: not the commit asked about.
	theirs, sha := commit("moved", "x", map[string]string{"a.txt": "v2\n"})
	require.NoError(t, c.Push(ctx, theirs, "origin", branch, scm.TokenAuth(""), true))
	same, err := c.SameContentAsHead(ctx, ours, "origin", branch, scm.TokenAuth(""), plumbing.ZeroHash.String())
	require.NoError(t, err)
	assert.False(t, same, "the branch is not at the commit asked about")
	same, err = c.SameContentAsHead(ctx, ours, "origin", branch, scm.TokenAuth(""), sha)
	require.NoError(t, err)
	assert.True(t, same)
}
