// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGitHubFlowHelpers: Commits and PushRemote work on GitHub too, so the
// TestCore_ tests that read a branch's commits or push by hand (the
// rendered-branch tests) run in the github suite. Commits reads the given
// branch of the shared repo; PushRemote is the shared repo over HTTPS with
// the suite's token.
func TestGitHubFlowHelpers(t *testing.T) {
	const get = "GET /repos/pnz1990/kardinal-demo/commits?sha=e2e%2Fns-env-test&per_page=2&page=1"
	f, srv := newFake(t, map[string]string{get: `[
		{"sha":"c2","commit":{"message":"render\n\nKardinal-Bundle: b2","author":{"name":"kardinal","email":"k@example.com"}}},
		{"sha":"c1","commit":{"message":"first","author":{"name":"dev","email":"d@example.com"}}}]`})
	s := server(t, "github", srv.URL, "pnz1990/kardinal-demo")
	r := Repo{Owner: "pnz1990", Name: "kardinal-demo", Branch: BranchPrefix + "ns"}

	got, err := Commits(context.Background(), s, r, "e2e/ns-env-test", 2)
	require.NoError(t, err)
	assert.Equal(t, []Commit{
		{SHA: "c2", Message: "render\n\nKardinal-Bundle: b2", AuthorName: "kardinal", AuthorEmail: "k@example.com"},
		{SHA: "c1", Message: "first", AuthorName: "dev", AuthorEmail: "d@example.com"},
	}, got)
	assert.Equal(t, []string{get}, f.seen)

	remote, token, err := PushRemote(s, r)
	require.NoError(t, err)
	assert.Equal(t, "http://git.example/pnz1990/kardinal-demo.git", remote)
	assert.Equal(t, "tok", token)
}
