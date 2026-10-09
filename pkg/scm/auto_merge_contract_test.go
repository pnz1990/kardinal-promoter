// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// body decodes the JSON body of a recorded call.
func body(t *testing.T, c apiCall) map[string]interface{} {
	t.Helper()
	if c.Body == "" {
		return nil
	}
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(c.Body), &m))
	return m
}

// lastPath is the path of the last call.
func lastPath(f *fakeAPI) string {
	c := f.Calls()
	return c[len(c)-1].Method + " " + c[len(c)-1].Path
}

// TestAutoMerge_GitHub: auto-merge through enablePullRequestAutoMerge with
// the method and message; a PR in clean status (nothing pending) is not
// merged unless allowImmediate, and then with the REST merge endpoint;
// GitHub Enterprise's GraphQL is /api/graphql; DisableAutoMerge uses
// disablePullRequestAutoMerge. Covers SCM-PRCTL-GH-01.
func TestAutoMerge_GitHub(t *testing.T) {
	ctx := context.Background()
	opts := scm.MergeOptions{Method: "squash", CommitTitle: "deploy (#5)", CommitBody: "Bundle b"}
	clean := apiReply{200, `{"data":null,"errors":[{"type":"UNPROCESSABLE","message":"Pull request Pull request is in clean status"}]}`}
	for _, prefix := range []string{"", "/api/v3"} {
		graphql := "POST /graphql"
		if prefix != "" {
			graphql = "POST /api/graphql"
		}
		f := newFakeAPI(t, map[string]apiReply{
			"GET " + prefix + "/repos/acme/web/pulls/5": {200, `{"node_id":"PR_kw5"}`},
			graphql: {200, `{"data":{"enablePullRequestAutoMerge":{"pullRequest":{"number":5}}}}`},
		})
		p, err := scm.NewProvider("github", "t", f.URL+prefix, "")
		require.NoError(t, err)
		c := p.(scm.PRController)
		require.NoError(t, c.EnableAutoMerge(ctx, "acme/web", 5, opts))
		calls := f.Calls()
		assert.Equal(t, graphql, calls[1].Method+" "+calls[1].Path)
		vars := body(t, calls[1])["variables"].(map[string]interface{})
		assert.Equal(t, map[string]interface{}{"id": "PR_kw5", "method": "SQUASH", "title": "deploy (#5)", "body": "Bundle b"}, vars)
		assert.Contains(t, body(t, calls[1])["query"], "enablePullRequestAutoMerge")

		f.queue(graphql, apiReply{200, `{"data":{}}`})
		require.NoError(t, c.DisableAutoMerge(ctx, "acme/web", 5))
		assert.Contains(t, body(t, f.Calls()[3])["query"], "disablePullRequestAutoMerge")
	}

	f := newFakeAPI(t, map[string]apiReply{
		"GET /repos/acme/web/pulls/5":       {200, `{"node_id":"PR_kw5"}`},
		"POST /graphql":                     clean,
		"PUT /repos/acme/web/pulls/5/merge": {200, `{"merged":true}`},
	})
	p, _ := scm.NewProvider("github", "t", f.URL, "")
	c := p.(scm.PRController)
	err := c.EnableAutoMerge(ctx, "acme/web", 5, opts)
	assert.ErrorIs(t, err, scm.ErrNothingPending)
	assert.Len(t, f.Calls(), 2, "no merge without allowImmediate")
	opts.AllowImmediate = true
	require.NoError(t, c.EnableAutoMerge(ctx, "acme/web", 5, opts))
	assert.Equal(t, "PUT /repos/acme/web/pulls/5/merge", lastPath(f))
	assert.Equal(t, map[string]interface{}{"merge_method": "squash", "commit_title": "deploy (#5)", "commit_message": "Bundle b"}, body(t, f.Calls()[4]))
}

// TestAutoMerge_GitLab: an MR GitLab is still checking is retried; one
// with something pending gets auto_merge (and merge_when_pipeline_succeeds)
// with squash and both commit messages; a mergeable MR has nothing pending
// and is merged only with allowImmediate, without the auto flags;
// DisableAutoMerge cancels it, a 406 (none) is not an error.
// Covers SCM-PRCTL-GL-01.
func TestAutoMerge_GitLab(t *testing.T) {
	ctx := context.Background()
	const mr = "/api/v4/projects/acme%2Fweb/merge_requests/3"
	f := newFakeAPI(t, map[string]apiReply{"PUT " + mr + "/merge": {200, `{}`}, "POST " + mr + "/cancel_merge_when_pipeline_succeeds": {406, `{"message":"406 Not Acceptable"}`}})
	p, _ := scm.NewProvider("gitlab", "t", f.URL, "")
	c := p.(scm.PRController)
	opts := scm.MergeOptions{Method: "squash", CommitTitle: "deploy (#3)"}

	f.queue("GET "+mr, apiReply{200, `{"detailed_merge_status":"checking"}`})
	err := c.EnableAutoMerge(ctx, "acme/web", 3, opts)
	assert.ErrorIs(t, err, scm.ErrMergeabilityUnknown)
	assert.True(t, scm.IsRetryableMergeError(err))

	f.queue("GET "+mr, apiReply{200, `{"detailed_merge_status":"ci_still_running"}`})
	require.NoError(t, c.EnableAutoMerge(ctx, "acme/web", 3, opts))
	assert.Equal(t, map[string]interface{}{"auto_merge": true, "merge_when_pipeline_succeeds": true, "squash": true,
		"merge_commit_message": "deploy (#3)", "squash_commit_message": "deploy (#3)"}, body(t, f.Calls()[2]))

	f.queue("GET "+mr, apiReply{200, `{"detailed_merge_status":"mergeable"}`})
	n := len(f.Calls())
	assert.ErrorIs(t, c.EnableAutoMerge(ctx, "acme/web", 3, opts), scm.ErrNothingPending)
	assert.Len(t, f.Calls(), n+1, "no merge without allowImmediate")

	opts.AllowImmediate = true
	f.queue("GET "+mr, apiReply{200, `{"detailed_merge_status":"mergeable"}`})
	require.NoError(t, c.EnableAutoMerge(ctx, "acme/web", 3, opts))
	assert.Equal(t, map[string]interface{}{"squash": true, "merge_commit_message": "deploy (#3)", "squash_commit_message": "deploy (#3)"},
		body(t, f.Calls()[len(f.Calls())-1]), "merged at once: no auto flags")

	require.NoError(t, c.DisableAutoMerge(ctx, "acme/web", 3))
}

// TestAutoMerge_Forgejo: the PR is scheduled (merge_when_checks_succeed)
// when a commit status is not success yet or base branch protection requires
// approvals or status checks; with nothing pending it is merged only with
// allowImmediate, without scheduling; DisableAutoMerge cancels the
// schedule, a 404 (none) is not an error. Covers SCM-PRCTL-FJ-01.
func TestAutoMerge_Forgejo(t *testing.T) {
	ctx := context.Background()
	const repo = "/api/v1/repos/acme/web"
	pr := apiReply{200, `{"head":{"sha":"h3ad"},"base":{"ref":"main"}}`}
	for _, tc := range []struct {
		name           string
		status, branch string
		allow          bool
		wantErr        error
		wantScheduled  bool
	}{
		{name: "pending status", status: `{"state":"pending","total_count":1}`, wantScheduled: true},
		{name: "required approvals", status: `{"state":"","total_count":0}`, branch: `{"protected":true,"required_approvals":1}`, wantScheduled: true},
		{name: "required status checks", status: `{"state":"success","total_count":1}`, branch: `{"protected":true,"enable_status_check":true}`, wantScheduled: true},
		{name: "nothing pending", status: `{"state":"success","total_count":2}`, branch: `{"protected":false}`, wantErr: scm.ErrNothingPending},
		{name: "nothing pending, allowImmediate", status: `{"state":"","total_count":0}`, branch: `{"protected":false}`, allow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t, map[string]apiReply{
				"GET " + repo + "/pulls/4":             pr,
				"GET " + repo + "/commits/h3ad/status": {200, tc.status},
				"GET " + repo + "/branches/main":       {200, tc.branch},
				"POST " + repo + "/pulls/4/merge":      {200, ``},
			})
			p, _ := scm.NewProvider("forgejo", "t", f.URL, "")
			err := p.(scm.PRController).EnableAutoMerge(ctx, "acme/web", 4,
				scm.MergeOptions{Method: "rebase", CommitTitle: "t", CommitBody: "b", AllowImmediate: tc.allow})
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.NotEqual(t, "POST "+repo+"/pulls/4/merge", lastPath(f))
				return
			}
			require.NoError(t, err)
			want := map[string]interface{}{"Do": "rebase", "delete_branch_after_merge": false, "MergeTitleField": "t", "MergeMessageField": "b"}
			if tc.wantScheduled {
				want["merge_when_checks_succeed"] = true
			}
			assert.Equal(t, want, body(t, f.Calls()[len(f.Calls())-1]))
		})
	}
	t.Run("already scheduled", func(t *testing.T) {
		f := newFakeAPI(t, map[string]apiReply{
			"GET " + repo + "/pulls/4":             pr,
			"GET " + repo + "/commits/h3ad/status": {200, `{"state":"pending","total_count":1}`},
			"POST " + repo + "/pulls/4/merge":      {409, `{"message":"pull request is already scheduled to auto merge when checks succeed [pull_id: 5]"}`},
		})
		p, _ := scm.NewProvider("forgejo", "t", f.URL, "")
		assert.NoError(t, p.(scm.PRController).EnableAutoMerge(ctx, "acme/web", 4, scm.MergeOptions{Method: "merge"}),
			"turning it on again is not an error")
	})
	f := newFakeAPI(t, map[string]apiReply{"DELETE " + repo + "/pulls/4/merge": {404, `{}`}})
	p, _ := scm.NewProvider("forgejo", "t", f.URL, "")
	require.NoError(t, p.(scm.PRController).DisableAutoMerge(ctx, "acme/web", 4))
}

// TestAutoMerge_AzureDevOps: auto-complete is set by the PR's creator when a
// policy evaluation is queued, running or rejected; with every policy
// approved (or none) nothing is pending and the PR is completed only with
// allowImmediate; DisableAutoMerge clears autoCompleteSetBy.
// Covers SCM-PRCTL-ADO-01.
func TestAutoMerge_AzureDevOps(t *testing.T) {
	ctx := context.Background()
	const pr = "/contoso/Web/_apis/git/repositories/web/pullrequests/12"
	const evals = "/contoso/Web/_apis/policy/evaluations"
	for _, tc := range []struct {
		name, evals string
		allow       bool
		wantErr     error
	}{
		{name: "policy running", evals: `{"value":[{"status":"approved"},{"status":"running"}]}`},
		{name: "nothing pending", evals: `{"value":[{"status":"approved"},{"status":"notApplicable"}]}`, wantErr: scm.ErrNothingPending},
		{name: "no policies, allowImmediate", evals: `{"value":[]}`, allow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t, map[string]apiReply{
				"GET " + pr:    {200, `{"createdBy":{"id":"creator-id"},"repository":{"project":{"id":"p-guid"}}}`},
				"GET " + evals: {200, tc.evals},
				"PATCH " + pr:  {200, `{}`},
			})
			p, _ := scm.NewProvider("azuredevops", "t", f.URL, "")
			err := p.(scm.PRController).EnableAutoMerge(ctx, "contoso/Web/web", 12, scm.MergeOptions{Method: "squash", AllowImmediate: tc.allow})
			assert.Equal(t, "vstfs:///CodeReview/CodeReviewId/p-guid/12", f.Calls()[1].Query.Get("artifactId"))
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Len(t, f.Calls(), 2)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, map[string]interface{}{"autoCompleteSetBy": map[string]interface{}{"id": "creator-id"},
				"completionOptions": map[string]interface{}{"mergeStrategy": "squash", "deleteSourceBranch": false}}, body(t, f.Calls()[2]))
		})
	}
	f := newFakeAPI(t, map[string]apiReply{"PATCH " + pr: {200, `{}`}})
	p, _ := scm.NewProvider("azuredevops", "t", f.URL, "")
	require.NoError(t, p.(scm.PRController).DisableAutoMerge(ctx, "contoso/Web/web", 12))
	assert.Equal(t, map[string]interface{}{"autoCompleteSetBy": map[string]interface{}{"id": "00000000-0000-0000-0000-000000000000"}}, body(t, f.Calls()[0]))
}

// TestIsRetryableMergeError: the SCM still checking, transient and
// mergeability refusals and network errors are retried; nothing pending,
// unsupported, permanent and GraphQL errors are not.
func TestIsRetryableMergeError(t *testing.T) {
	assert.True(t, scm.IsRetryableMergeError(scm.ErrMergeabilityUnknown))
	assert.True(t, scm.IsRetryableMergeError(&scm.APIError{StatusCode: http.StatusMethodNotAllowed}))
	assert.True(t, scm.IsRetryableMergeError(&scm.APIError{StatusCode: 502, Transient: true}))
	assert.True(t, scm.IsRetryableMergeError(&net.OpError{Op: "dial", Err: errors.New("refused")}))
	assert.False(t, scm.IsRetryableMergeError(scm.ErrNothingPending))
	assert.False(t, scm.IsRetryableMergeError(scm.ErrPRControlUnsupported))
	assert.False(t, scm.IsRetryableMergeError(&scm.APIError{StatusCode: http.StatusForbidden}))
	assert.False(t, scm.IsRetryableMergeError(errors.New("GitHub GraphQL: Auto merge is not allowed for this repository")))
}
