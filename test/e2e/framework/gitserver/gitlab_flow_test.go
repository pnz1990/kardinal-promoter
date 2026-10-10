// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGitLabFlowHelpers: Commits and PushRemote work on GitLab too, so the
// TestCore_ tests that read a branch's commits or push by hand (the
// rendered-branch tests) run in the gitlab suite.
func TestGitLabFlowHelpers(t *testing.T) {
	const get = "GET /api/v4/projects/e2e%2Fr/repository/commits?ref_name=env%2Ftest&per_page=2&page=1"
	f, srv := newFake(t, map[string]string{get: `[
		{"id":"c2","message":"render\n\nKardinal-Bundle: b2","author_name":"kardinal","author_email":"k@example.com"},
		{"id":"c1","message":"first","author_name":"dev","author_email":"d@example.com"}]`})
	s := server(t, "gitlab", srv.URL, "")
	r := Repo{Owner: "e2e", Name: "r", Branch: "main"}

	got, err := Commits(context.Background(), s, r, "env/test", 2)
	require.NoError(t, err)
	assert.Equal(t, []Commit{
		{SHA: "c2", Message: "render\n\nKardinal-Bundle: b2", AuthorName: "kardinal", AuthorEmail: "k@example.com"},
		{SHA: "c1", Message: "first", AuthorName: "dev", AuthorEmail: "d@example.com"},
	}, got)
	assert.Equal(t, []string{get}, f.seen)

	remote, token, err := PushRemote(s, r)
	require.NoError(t, err)
	assert.Equal(t, srv.URL+"/e2e/r.git", remote)
	assert.NotEmpty(t, token)
}
