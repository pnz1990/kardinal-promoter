// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The request shapes the live SCM suites rely on: PR authors and merge
// commits, branches, PRs the test runner opens, users and GitLab's extras.

func TestForgejoSCM(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t, map[string]string{
		"GET /api/v1/repos/e2e/r/pulls?state=all&limit=50&page=1": `[
			{"number":1,"state":"closed","merged":true,"merge_commit_sha":"m1","user":{"login":"bot"},"head":{"ref":"k","sha":"h1"},"base":{"ref":"main"}},
			{"number":2,"state":"open","merge_commit_sha":"","user":{"login":"bot"},"head":{"ref":"k2","sha":"h2"},"base":{"ref":"main"}}]`,
		"POST /api/v1/repos/e2e/r/branches":             `{}`,
		"GET /api/v1/repos/e2e/r/branches/release/x":    `{"commit":{"id":"c9"}}`,
		"DELETE /api/v1/repos/e2e/r/branches/release/x": `{}`,
		"POST /api/v1/repos/e2e/r/pulls":                `{"number":3,"state":"open","user":{"login":"admin"},"head":{"ref":"k"},"base":{"ref":"release/x"}}`,
		"POST /api/v1/repos/e2e/r/pulls/2/reviews":      `{}`,
		"POST /api/v1/admin/users":                      `{}`,
		"POST /api/v1/users/u1/tokens":                  `{"sha1":"secret-token"}`,
		"DELETE /api/v1/admin/users/u1?purge=true":      `{}`,
		"PUT /api/v1/repos/e2e/r/collaborators/u1":      `{}`,
	})
	s := server(t, "forgejo", srv.URL, "")
	r := Repo{Owner: "e2e", Name: "r", Branch: "main"}

	prs, err := s.PullRequests(ctx, r)
	require.NoError(t, err)
	require.Len(t, prs, 2)
	assert.Equal(t, PR{Number: 1, State: "merged", Head: "k", Base: "main", Author: "bot", HeadSHA: "h1", MergeCommit: "m1"}, prs[0])
	assert.Empty(t, prs[1].MergeCommit)

	b := s.(Brancher)
	require.NoError(t, b.CreateBranch(ctx, r, "release/x", "main"))
	assert.Equal(t, map[string]interface{}{"new_branch_name": "release/x", "old_branch_name": "main"},
		f.bodies["POST /api/v1/repos/e2e/r/branches"])
	head, err := b.BranchHead(ctx, r, "release/x")
	require.NoError(t, err)
	assert.Equal(t, "c9", head)
	pr, err := b.OpenPR(ctx, r, "k", "release/x", "other base")
	require.NoError(t, err)
	assert.Equal(t, PR{Number: 3, State: "open", Head: "k", Base: "release/x", Author: "admin"}, pr)
	assert.Equal(t, "release/x", f.bodies["POST /api/v1/repos/e2e/r/pulls"]["base"])
	require.NoError(t, b.DeleteBranch(ctx, r, "release/x"))
	require.NoError(t, b.DeleteBranch(ctx, r, "gone"), "404 on delete is success")

	require.NoError(t, s.(Reviewer).ApprovePR(ctx, r, 2, "lgtm"))
	assert.Equal(t, "APPROVED", f.bodies["POST /api/v1/repos/e2e/r/pulls/2/reviews"]["event"])

	u := s.(Users)
	tok, err := u.CreateUser(ctx, "u1", []string{"write:repository"})
	require.NoError(t, err)
	assert.Equal(t, "secret-token", tok)
	assert.Equal(t, "u1", f.bodies["POST /api/v1/admin/users"]["username"])
	assert.Equal(t, []interface{}{"write:repository"}, f.bodies["POST /api/v1/users/u1/tokens"]["scopes"])
	assert.Contains(t, f.header.Get("Authorization"), "Basic ", "tokens are made with the user's basic auth")
	require.NoError(t, u.AddCollaborator(ctx, r, "u1"))
	assert.Equal(t, "write", f.bodies["PUT /api/v1/repos/e2e/r/collaborators/u1"]["permission"])
	require.NoError(t, u.DeleteUser(ctx, "u1"))
	assert.Equal(t, "token tok", f.header.Get("Authorization"), "the admin token is back after CreateUser")
}

func TestGitLabSCM(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t, map[string]string{
		"GET /api/v4/projects/e2e%2Fr/merge_requests?state=all&target_branch=main&per_page=100&page=1": `[
			{"iid":1,"state":"merged","sha":"h1","merge_commit_sha":"m1","author":{"username":"kardinal-bot"},"source_branch":"k","target_branch":"main"},
			{"iid":2,"state":"merged","sha":"h2","merge_commit_sha":null,"squash_commit_sha":"s2","source_branch":"k","target_branch":"main"},
			{"iid":3,"state":"merged","sha":"h3","merge_commit_sha":null,"source_branch":"k","target_branch":"main"},
			{"iid":4,"state":"opened","sha":"h4","source_branch":"k","target_branch":"main"}]`,
		"PUT /api/v4/projects/e2e%2Fr/merge_requests/4/merge":                           `{}`,
		"POST /api/v4/projects/e2e%2Fr/merge_requests/4/approve":                        `{}`,
		"POST /api/v4/projects/e2e%2Fr/merge_requests/4/notes":                          `{}`,
		"PUT /api/v4/projects/e2e%2Fr":                                                  `{}`,
		"POST /api/v4/projects/e2e%2Fr/repository/branches?branch=release%2Fx&ref=main": `{}`,
		"GET /api/v4/projects/e2e%2Fr/repository/branches/release%2Fx":                  `{"commit":{"id":"c9"}}`,
		"DELETE /api/v4/projects/e2e%2Fr/repository/branches/release%2Fx":               `{}`,
		"POST /api/v4/projects/e2e%2Fr/merge_requests":                                  `{"iid":5,"state":"opened","source_branch":"k","target_branch":"release/x"}`,
		"GET /api/v4/groups/e2e":                                                        `{"id":3}`,
		"POST /api/v4/groups":                                                           `{}`,
		"GET /api/v4/namespaces/e2e%2Fsub":                                              `{"id":11}`,
		"POST /api/v4/projects":                                                         `{}`,
		"POST /api/v4/projects/e2e%2Fsub%2Fr/repository/commits":                        `{}`,
		"POST /api/v4/users":                                                            `{"id":21}`,
		"POST /api/v4/users/21/personal_access_tokens":                                  `{"token":"glpat-x"}`,
		"GET /api/v4/users?username=u1":                                                 `[{"id":21}]`,
		"GET /api/v4/users?username=nobody":                                             `[]`,
		"DELETE /api/v4/users/21?hard_delete=true":                                      `{}`,
		"POST /api/v4/projects/e2e%2Fr/members":                                         `{}`,
		"DELETE /api/v4/projects/e2e%2Fr":                                               `{}`,
	})
	s := server(t, "gitlab", srv.URL, "")
	s.(*gitlab).retry = time.Millisecond
	r := Repo{Owner: "e2e", Name: "r", Branch: "main"}

	prs, err := s.PullRequests(ctx, r)
	require.NoError(t, err)
	require.Len(t, prs, 4)
	assert.Equal(t, "kardinal-bot", prs[0].Author)
	assert.Equal(t, []string{"m1", "s2", "h3", ""}, []string{prs[0].MergeCommit, prs[1].MergeCommit, prs[2].MergeCommit, prs[3].MergeCommit},
		"merge commit, else squash commit, else (fast-forward) head; none while open")

	f.fail["PUT /api/v4/projects/e2e%2Fr/merge_requests/4/merge"] = []int{http.StatusMethodNotAllowed, http.StatusUnprocessableEntity}
	require.NoError(t, s.MergePR(ctx, r, 4), "merge retried while GitLab checks the MR")
	f.fail["PUT /api/v4/projects/e2e%2Fr/merge_requests/4/merge"] = []int{http.StatusForbidden}
	require.Error(t, s.MergePR(ctx, r, 4), "403 is not retried")

	require.NoError(t, s.(Reviewer).ApprovePR(ctx, r, 4, "lgtm"))
	assert.Equal(t, "lgtm", f.bodies["POST /api/v4/projects/e2e%2Fr/merge_requests/4/notes"]["body"])
	require.NoError(t, s.(FastForwarder).SetFastForwardMerge(ctx, r))
	assert.Equal(t, "ff", f.bodies["PUT /api/v4/projects/e2e%2Fr"]["merge_method"])

	b := s.(Brancher)
	require.NoError(t, b.CreateBranch(ctx, r, "release/x", "main"))
	head, err := b.BranchHead(ctx, r, "release/x")
	require.NoError(t, err)
	assert.Equal(t, "c9", head)
	pr, err := b.OpenPR(ctx, r, "k", "release/x", "other base")
	require.NoError(t, err)
	assert.Equal(t, PR{Number: 5, State: "open", Head: "k", Base: "release/x"}, pr)
	require.NoError(t, b.DeleteBranch(ctx, r, "release/x"))

	f.fail["GET /api/v4/groups/e2e%2Fsub"] = []int{http.StatusNotFound}
	f.routes["GET /api/v4/groups/e2e%2Fsub"] = `{"id":11}`
	sr, err := s.(Subgrouper).CreateSubgroupRepo(ctx, "sub", "r", map[string][]byte{"a": []byte("a")})
	require.NoError(t, err)
	assert.Equal(t, Repo{Owner: "e2e/sub", Name: "r", Branch: "main", CloneURL: "http://git.example/e2e/sub/r.git"}, sr)
	assert.EqualValues(t, 3, f.bodies["POST /api/v4/groups"]["parent_id"])
	assert.EqualValues(t, 11, f.bodies["POST /api/v4/projects"]["namespace_id"])

	u := s.(Users)
	tok, err := u.CreateUser(ctx, "u1", []string{"read_api"})
	require.NoError(t, err)
	assert.Equal(t, "glpat-x", tok)
	assert.Equal(t, []interface{}{"read_api"}, f.bodies["POST /api/v4/users/21/personal_access_tokens"]["scopes"])
	require.NoError(t, u.AddCollaborator(ctx, r, "u1"))
	assert.EqualValues(t, 30, f.bodies["POST /api/v4/projects/e2e%2Fr/members"]["access_level"])
	require.NoError(t, u.DeleteUser(ctx, "u1"))
	require.NoError(t, u.DeleteUser(ctx, "nobody"), "a missing user is already deleted")

	del := "DELETE /api/v4/projects/e2e%2Fr"
	f.fail[del] = []int{http.StatusInternalServerError, http.StatusInternalServerError}
	require.NoError(t, s.DeleteRepo(ctx, r), "a 500 from a delete racing a push job is retried")
	f.fail[del] = []int{http.StatusInternalServerError, http.StatusInternalServerError, http.StatusInternalServerError}
	require.Error(t, s.DeleteRepo(ctx, r), "the delete gives up after 3 tries")
	f.fail[del] = []int{http.StatusNotFound}
	require.NoError(t, s.DeleteRepo(ctx, r), "a missing project is already deleted")
	f.fail[del] = []int{http.StatusForbidden}
	require.Error(t, s.DeleteRepo(ctx, r), "403 is not retried")
}

func TestGitHubSCM(t *testing.T) {
	ctx := context.Background()
	f, srv := newFake(t, map[string]string{
		"GET /repos/o/demo/pulls?state=all&base=e2e%2Fr&per_page=100&page=1": `[
			{"number":8,"state":"open","merge_commit_sha":"test-merge","user":{"login":"me"},"head":{"ref":"k","sha":"h8"},"base":{"ref":"e2e/r"}},
			{"number":9,"state":"closed","merged_at":"2026-09-30T00:00:00Z","merge_commit_sha":"m9","head":{"ref":"k"},"base":{"ref":"e2e/r"}}]`,
		"GET /repos/o/demo/git/ref/heads/e2e/r":           `{"object":{"sha":"c1"}}`,
		"POST /repos/o/demo/git/refs":                     `{}`,
		"DELETE /repos/o/demo/git/refs/heads/e2e/r-other": `{}`,
		"POST /repos/o/demo/pulls":                        `{"number":10,"state":"open","user":{"login":"me"},"head":{"ref":"k"},"base":{"ref":"e2e/r-other"}}`,
	})
	s := server(t, "github", srv.URL, "o/demo")
	r := Repo{Owner: "o", Name: "demo", Branch: "e2e/r"}

	prs, err := s.PullRequests(ctx, r)
	require.NoError(t, err)
	require.Len(t, prs, 2)
	assert.Equal(t, PR{Number: 8, State: "open", Head: "k", Base: "e2e/r", Author: "me", HeadSHA: "h8"}, prs[0],
		"an open PR's test merge commit is not its merge commit")
	assert.Equal(t, "m9", prs[1].MergeCommit)

	b := s.(Brancher)
	require.NoError(t, b.CreateBranch(ctx, r, "e2e/r-other", "e2e/r"))
	assert.Equal(t, map[string]interface{}{"ref": "refs/heads/e2e/r-other", "sha": "c1"}, f.bodies["POST /repos/o/demo/git/refs"])
	pr, err := b.OpenPR(ctx, r, "k", "e2e/r-other", "other base")
	require.NoError(t, err)
	assert.Equal(t, 10, pr.Number)
	require.NoError(t, b.DeleteBranch(ctx, r, "e2e/r-other"))

	f.seen = nil
	require.ErrorContains(t, b.CreateBranch(ctx, r, "release", "e2e/r"), "refusing")
	require.ErrorContains(t, b.DeleteBranch(ctx, r, "main"), "refusing")
	require.ErrorContains(t, b.DeleteBranch(ctx, r, "kardinal/nginx-demo-c7ntq/prod"), "refusing")
	assert.Empty(t, f.seen, "nothing outside e2e/ is touched")
}
