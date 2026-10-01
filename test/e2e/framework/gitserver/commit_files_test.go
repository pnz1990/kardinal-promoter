// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitLabCommitFiles(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t, map[string]string{
		"GET /api/v4/projects/e2e%2Fr/repository/files/a.yaml?ref=main": `{}`,
		"POST /api/v4/projects/e2e%2Fr/repository/commits":              `{"id":"c7"}`,
	})
	s := server(t, "gitlab", srv.URL, "")
	r := Repo{Owner: "e2e", Name: "r", Branch: "main"}
	files := map[string][]byte{"dir/b.yaml": []byte("b"), "a.yaml": []byte("a")}

	sha, err := CommitFiles(ctx, s, r, "main", "", "msg", files)
	require.NoError(t, err)
	assert.Equal(t, "c7", sha)
	assert.Contains(t, f.seen, "GET /api/v4/projects/e2e%2Fr/repository/files/dir%2Fb.yaml?ref=main")
	body := f.bodies["POST /api/v4/projects/e2e%2Fr/repository/commits"]
	assert.Equal(t, "main", body["branch"])
	assert.Equal(t, "msg", body["commit_message"])
	assert.NotContains(t, body, "start_branch")
	assert.Equal(t, []interface{}{
		map[string]interface{}{"action": "update", "file_path": "a.yaml", "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString([]byte("a"))},
		map[string]interface{}{"action": "create", "file_path": "dir/b.yaml", "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString([]byte("b"))},
	}, body["actions"], "an existing file is updated, a missing one created, in path order")

	_, err = CommitFiles(ctx, s, r, "main", "feature", "msg", map[string][]byte{"a.yaml": []byte("a")})
	require.NoError(t, err)
	body = f.bodies["POST /api/v4/projects/e2e%2Fr/repository/commits"]
	assert.Equal(t, "feature", body["branch"])
	assert.Equal(t, "main", body["start_branch"], "the new branch starts at branch")

	f.routes["POST /api/v4/projects/e2e%2Fr/repository/commits"] = `{}`
	_, err = CommitFiles(ctx, s, r, "main", "", "msg", map[string][]byte{"a.yaml": []byte("a")})
	require.ErrorContains(t, err, "no commit SHA")
}

func TestGitHubCommitFiles(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t, map[string]string{
		"GET /repos/o/demo/git/ref/heads/e2e/r":    `{"object":{"sha":"p1"}}`,
		"GET /repos/o/demo/git/commits/p1":         `{"tree":{"sha":"t0"}}`,
		"POST /repos/o/demo/git/trees":             `{"sha":"t1"}`,
		"POST /repos/o/demo/git/commits":           `{"sha":"c1"}`,
		"PATCH /repos/o/demo/git/refs/heads/e2e/r": `{}`,
		"POST /repos/o/demo/git/refs":              `{}`,
		// Answered, so only the prefix check stops a write to main.
		"GET /repos/o/demo/git/ref/heads/main":    `{"object":{"sha":"m1"}}`,
		"GET /repos/o/demo/git/commits/m1":        `{"tree":{"sha":"t9"}}`,
		"PATCH /repos/o/demo/git/refs/heads/main": `{}`,
	})
	s := server(t, "github", srv.URL, "o/demo")
	r := Repo{Owner: "o", Name: "demo", Branch: "e2e/r"}

	sha, err := CommitFiles(ctx, s, r, "e2e/r", "", "msg", map[string][]byte{"b.yaml": []byte("b"), "a.yaml": []byte("a")})
	require.NoError(t, err)
	assert.Equal(t, "c1", sha)
	assert.Equal(t, []string{
		"GET /repos/o/demo/git/ref/heads/e2e/r",
		"GET /repos/o/demo/git/commits/p1",
		"POST /repos/o/demo/git/trees",
		"POST /repos/o/demo/git/commits",
		"PATCH /repos/o/demo/git/refs/heads/e2e/r",
	}, f.seen)
	tree := f.bodies["POST /repos/o/demo/git/trees"]
	assert.Equal(t, "t0", tree["base_tree"], "the files go on top of the branch's tree")
	assert.Equal(t, []interface{}{
		map[string]interface{}{"path": "a.yaml", "mode": "100644", "type": "blob", "content": "a"},
		map[string]interface{}{"path": "b.yaml", "mode": "100644", "type": "blob", "content": "b"},
	}, tree["tree"])
	assert.Equal(t, map[string]interface{}{"message": "msg", "tree": "t1", "parents": []interface{}{"p1"}},
		f.bodies["POST /repos/o/demo/git/commits"])
	assert.Equal(t, map[string]interface{}{"sha": "c1", "force": false},
		f.bodies["PATCH /repos/o/demo/git/refs/heads/e2e/r"], "the branch only moves forward")

	f.seen = nil
	_, err = CommitFiles(ctx, s, r, "e2e/r", "e2e/r-new", "msg", map[string][]byte{"a.yaml": []byte("a")})
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{"ref": "refs/heads/e2e/r-new", "sha": "c1"}, f.bodies["POST /repos/o/demo/git/refs"])
	assert.NotContains(t, f.seen, "PATCH /repos/o/demo/git/refs/heads/e2e/r", "branch stays where it was")

	f.seen = nil
	_, err = CommitFiles(ctx, s, r, "main", "", "msg", map[string][]byte{"a.yaml": []byte("a")})
	require.ErrorContains(t, err, "refusing")
	_, err = CommitFiles(ctx, s, r, "e2e/r", "kardinal/x", "msg", map[string][]byte{"a.yaml": []byte("a")})
	require.ErrorContains(t, err, "refusing")
	assert.Empty(t, f.seen, "nothing outside e2e/ is written")
}
