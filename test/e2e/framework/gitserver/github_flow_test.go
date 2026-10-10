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

// TestGitHubPushAllowed: on the shared GitHub repo a test may push only its
// own branches (under BranchPrefix) and the PR branches kardinal opens for
// its namespace (PRBranchPrefix, the prHead format); not the default
// branch, another namespace's kardinal branches or any other branch.
func TestGitHubPushAllowed(t *testing.T) {
	r := Repo{Owner: "pnz1990", Name: "kardinal-demo", Branch: BranchPrefix + "e2e-ns-1"}
	mine := PRBranchPrefix("e2e-ns-1")
	assert.Equal(t, "kardinal/", mine[:9])
	assert.Len(t, mine, len("kardinal/")+8+1)
	for branch, want := range map[string]bool{
		"e2e/e2e-ns-1":                   true,
		"e2e/e2e-ns-1-env-test":          true,
		mine + "podinfo-abc/prod":        true,
		PRBranchPrefix("other") + "b/p":  false,
		"kardinal/nginx-demo-lcclw/prod": false,
		"main":                           false,
		"env/test":                       false,
	} {
		assert.Equal(t, want, GitHubPushAllowed(r, branch), branch)
	}
	assert.False(t, GitHubPushAllowed(Repo{Branch: "main"}, "e2e/x"), "a repo that is not a test branch allows nothing")
}

// TestGitHubDeleteRepoHeads: DeleteRepo closes every open PR into the test's
// branch but deletes only the head branches a test or kardinal made (under
// e2e/ or kardinal/): a PR opened into it from another branch keeps its
// head.
func TestGitHubDeleteRepoHeads(t *testing.T) {
	f, srv := newFake(t, map[string]string{
		"GET /repos/o/demo/pulls?state=all&base=e2e%2Fr&per_page=100&page=1": `[
			{"number":1,"state":"open","head":{"ref":"kardinal/1234abcd/b/prod"},"base":{"ref":"e2e/r"}},
			{"number":2,"state":"open","head":{"ref":"e2e/r-env-prod"},"base":{"ref":"e2e/r"}},
			{"number":3,"state":"open","head":{"ref":"someones-feature"},"base":{"ref":"e2e/r"}}]`,
		"PATCH /repos/o/demo/pulls/1": `{}`, "PATCH /repos/o/demo/pulls/2": `{}`, "PATCH /repos/o/demo/pulls/3": `{}`,
		"DELETE /repos/o/demo/git/refs/heads/kardinal/1234abcd/b/prod": `{}`,
		"DELETE /repos/o/demo/git/refs/heads/e2e/r-env-prod":           `{}`,
		"DELETE /repos/o/demo/git/refs/heads/e2e/r":                    `{}`,
	})
	s := server(t, "github", srv.URL, "o/demo")
	require.NoError(t, s.DeleteRepo(context.Background(), Repo{Owner: "o", Name: "demo", Branch: "e2e/r"}))
	assert.Contains(t, f.seen, "DELETE /repos/o/demo/git/refs/heads/kardinal/1234abcd/b/prod")
	assert.Contains(t, f.seen, "DELETE /repos/o/demo/git/refs/heads/e2e/r-env-prod")
	assert.Contains(t, f.seen, "PATCH /repos/o/demo/pulls/3", "the foreign PR is closed")
	assert.NotContains(t, f.seen, "DELETE /repos/o/demo/git/refs/heads/someones-feature", "its head branch is kept")
}
