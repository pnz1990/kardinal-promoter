// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

// apiCall is one request the fake API got: "METHOD /escaped/path?query" and
// its JSON body.
type apiCall struct {
	Route string
	Body  map[string]interface{}
}

// apiReply is the answer to a route.
type apiReply struct {
	Status int
	Body   string
}

// routedAPI is a fake SCM API that answers each "METHOD /escaped/path"
// (query excluded) from routes; a route with several replies answers with
// them in turn and repeats the last. Unknown routes get 404.
func routedAPI(t *testing.T, routes map[string][]apiReply) (*httptest.Server, func() []apiCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []apiCall
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		route := r.Method + " " + r.URL.EscapedPath()
		c := apiCall{Route: route}
		if r.URL.RawQuery != "" {
			c.Route += "?" + r.URL.RawQuery
		}
		if len(raw) > 0 {
			assert.NoError(t, json.Unmarshal(raw, &c.Body), "%s body", route)
		}
		mu.Lock()
		calls = append(calls, c)
		replies := routes[route]
		n := seen[route]
		seen[route]++
		mu.Unlock()
		if len(replies) == 0 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"not found"}`)
			return
		}
		if n >= len(replies) {
			n = len(replies) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(replies[n].Status)
		_, _ = io.WriteString(w, replies[n].Body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []apiCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]apiCall(nil), calls...)
	}
}

// routes lists the routes of calls, in order.
func routes(calls []apiCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Route)
	}
	return out
}

// find returns the first call to route.
func find(t *testing.T, calls []apiCall, route string) apiCall {
	t.Helper()
	for _, c := range calls {
		if c.Route == route {
			return c
		}
	}
	t.Fatalf("no call to %s in %v", route, routes(calls))
	return apiCall{}
}

// fullPRConfig asks for every control, with templates over the Bundle.
func fullPRConfig(method string) *v1alpha1.PRConfig {
	return &v1alpha1.PRConfig{
		TitleTemplate: "deploy {{ .Bundle.Version }} to {{ .Environment }}",
		BodyTemplate:  "by {{ .Bundle.Author }}\n{{ gatesTable }}",
		Labels:        []string{"env/{{ .Environment }}"},
		Reviewers:     []string{"alice"},
		TeamReviewers: []string{"platform"},
		Assignees:     []string{"{{ .Bundle.Author }}"},
		Merge: &v1alpha1.PRMergeConfig{Auto: true, Method: method,
			CommitMessageTemplate: "{{ .PR.Title }} (#{{ .PR.Number }})\n\nBundle {{ .Bundle.Name }}"},
	}
}

// controlsState is the open-pr state of a pr-review prod environment with
// cfg, on gitURL, opened by provider p.
func controlsState(t *testing.T, gitURL string, p scm.SCMProvider, cfg *v1alpha1.PRConfig) *parentsteps.StepState {
	t.Helper()
	state := openPRState(t, gitURL, p, "")
	state.Bundle.Provenance = &v1alpha1.BundleProvenance{Author: "octocat", CommitSHA: "abc123"}
	state.Environment.PR = cfg
	return state
}

func runOpenPR(t *testing.T, state *parentsteps.StepState) (parentsteps.StepResult, error) {
	t.Helper()
	step, err := parentsteps.Lookup("open-pr")
	require.NoError(t, err)
	return step.Execute(context.Background(), state)
}

// TestOpenPRControls_GitHub: the PR gets the rendered title and body, the
// labels, reviewers, team reviewers and assignees, and auto-merge through
// GraphQL with the merge method and rendered commit message; a PR GitHub
// says needs nothing more is merged at once with the REST endpoint; GitHub
// Enterprise's GraphQL endpoint is /api/graphql. Covers SCM-PRCTL-GH-01.
func TestOpenPRControls_GitHub(t *testing.T) {
	defer steps.SetAutoMergeRetryDelays([]time.Duration{0})()
	const (
		pulls = "POST /repos/acme/web/pulls"
		pr    = "GET /repos/acme/web/pulls/5"
	)
	base := map[string][]apiReply{
		pulls:                                  {{201, `{"number":5,"html_url":"https://github.com/acme/web/pull/5"}`}},
		"POST /repos/acme/web/issues/5/labels": {{200, `[]`}},
		"POST /repos/acme/web/pulls/5/requested_reviewers": {{201, `{}`}},
		"POST /repos/acme/web/issues/5/assignees":          {{201, `{}`}},
		pr: {{200, `{"node_id":"PR_kw5"}`}},
	}
	tests := []struct {
		name, prefix, graphql string
		graphqlReply          string
		wantMerge             bool
	}{
		{name: "auto-merge", graphql: "POST /graphql", graphqlReply: `{"data":{"enablePullRequestAutoMerge":{"pullRequest":{"number":5}}}}`},
		{name: "clean status merges at once", graphql: "POST /graphql",
			graphqlReply: `{"data":null,"errors":[{"type":"UNPROCESSABLE","message":"Pull request Pull request is in clean status"}]}`, wantMerge: true},
		{name: "enterprise", prefix: "/api/v3", graphql: "POST /api/graphql", graphqlReply: `{"data":{}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := map[string][]apiReply{tt.graphql: {{200, tt.graphqlReply}}}
			for k, v := range base {
				method, path, _ := strings.Cut(k, " ")
				rs[method+" "+tt.prefix+path] = v
			}
			rs["PUT "+tt.prefix+"/repos/acme/web/pulls/5/merge"] = []apiReply{{200, `{"merged":true}`}}
			srv, calls := routedAPI(t, rs)
			p, err := scm.NewProvider("github", "ghp", srv.URL+tt.prefix, "")
			require.NoError(t, err)

			result, err := runOpenPR(t, controlsState(t, "https://github.com/acme/web.git", p, fullPRConfig("squash")))
			require.NoError(t, err)
			assert.Equal(t, parentsteps.StepSuccess, result.Status, result.Message)
			assert.Equal(t, "enabled", result.Outputs[parentsteps.OutputPRAutoMerge])
			assert.NotContains(t, result.Message, "failed")

			got := calls()
			want := []string{"POST " + tt.prefix + "/repos/acme/web/pulls", "POST " + tt.prefix + "/repos/acme/web/issues/5/labels",
				"POST " + tt.prefix + "/repos/acme/web/pulls/5/requested_reviewers", "POST " + tt.prefix + "/repos/acme/web/issues/5/assignees",
				"GET " + tt.prefix + "/repos/acme/web/pulls/5", tt.graphql}
			if tt.wantMerge {
				want = append(want, "PUT "+tt.prefix+"/repos/acme/web/pulls/5/merge")
			}
			assert.Equal(t, want, routes(got))

			open := got[0].Body
			assert.Equal(t, "deploy 1.29.0 to prod", open["title"])
			assert.True(t, strings.HasPrefix(open["body"].(string), "<!-- kardinal-promoter auto-generated PR -->\nby octocat\n### Policy Gate Compliance"), open["body"])
			assert.Equal(t, []interface{}{"kardinal", "kardinal/promotion", "env/prod"}, got[1].Body["labels"])
			assert.Equal(t, []interface{}{"alice"}, got[2].Body["reviewers"])
			assert.Equal(t, []interface{}{"platform"}, got[2].Body["team_reviewers"])
			assert.Equal(t, []interface{}{"octocat"}, got[3].Body["assignees"])
			vars := got[5].Body["variables"].(map[string]interface{})
			assert.Equal(t, map[string]interface{}{"id": "PR_kw5", "method": "SQUASH",
				"title": "deploy 1.29.0 to prod (#5)", "body": "Bundle nginx-demo-v1-29-0"}, vars)
			assert.Contains(t, got[5].Body["query"], "enablePullRequestAutoMerge")
			if tt.wantMerge {
				assert.Equal(t, map[string]interface{}{"merge_method": "squash", "commit_title": "deploy 1.29.0 to prod (#5)",
					"commit_message": "Bundle nginx-demo-v1-29-0"}, got[6].Body)
			}
		})
	}
}

// TestOpenPRControls_GitLab: reviewers and assignees are set by user ID,
// keeping the ones the MR has, and auto-merge squashes with the rendered
// squash commit message. Covers SCM-PRCTL-GL-01.
func TestOpenPRControls_GitLab(t *testing.T) {
	defer steps.SetAutoMergeRetryDelays([]time.Duration{0})()
	const mr = "/api/v4/projects/acme%2Fweb/merge_requests/3"
	srv, calls := routedAPI(t, map[string][]apiReply{
		"POST /api/v4/projects/acme%2Fweb/merge_requests": {{201, `{"iid":3,"web_url":"https://gitlab.com/acme/web/-/merge_requests/3"}`}},
		"PUT " + mr:            {{200, `{}`}},
		"GET " + mr:            {{200, `{"reviewers":[{"id":7}],"assignees":[]}`}},
		"GET /api/v4/users":    {{200, `[{"id":11,"username":"x"}]`}},
		"PUT " + mr + "/merge": {{405, `{"message":"405 Method Not Allowed"}`}, {200, `{"state":"opened","merge_when_pipeline_succeeds":true}`}},
	})
	p, err := scm.NewProvider("gitlab", "glpat", srv.URL, "")
	require.NoError(t, err)
	cfg := fullPRConfig("squash")
	cfg.TeamReviewers = nil

	result, err := runOpenPR(t, controlsState(t, "https://gitlab.com/acme/web.git", p, cfg))
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status, result.Message)
	assert.Equal(t, "enabled", result.Outputs[parentsteps.OutputPRAutoMerge], "the 405 of a merge request GitLab is still checking is retried")

	got := calls()
	assert.Equal(t, []string{
		"POST /api/v4/projects/acme%2Fweb/merge_requests",
		"PUT " + mr,                                                  // labels
		"GET " + mr, "GET /api/v4/users?username=alice", "PUT " + mr, // reviewers
		"GET " + mr, "GET /api/v4/users?username=octocat", "PUT " + mr, // assignees
		"PUT " + mr + "/merge", "PUT " + mr + "/merge",
	}, routes(got))
	assert.Equal(t, "kardinal,kardinal/promotion,env/prod", got[1].Body["add_labels"])
	assert.Equal(t, []interface{}{float64(7), float64(11)}, got[4].Body["reviewer_ids"], "the MR's reviewer is kept")
	assert.Equal(t, []interface{}{float64(11)}, got[7].Body["assignee_ids"])
	assert.Equal(t, map[string]interface{}{"auto_merge": true, "merge_when_pipeline_succeeds": true, "squash": true,
		"squash_commit_message": "deploy 1.29.0 to prod (#3)\n\nBundle nginx-demo-v1-29-0",
		"merge_commit_message":  "deploy 1.29.0 to prod (#3)\n\nBundle nginx-demo-v1-29-0"}, got[9].Body)

	t.Run("team reviewers refused before the MR", func(t *testing.T) {
		srv, calls := routedAPI(t, nil)
		p, err := scm.NewProvider("gitlab", "glpat", srv.URL, "")
		require.NoError(t, err)
		result, err := runOpenPR(t, controlsState(t, "https://gitlab.com/acme/web.git", p, &v1alpha1.PRConfig{TeamReviewers: []string{"t"}}))
		require.Error(t, err)
		assert.ErrorIs(t, err, parentsteps.ErrPermanent)
		assert.Equal(t, parentsteps.StepFailed, result.Status)
		assert.Equal(t, "environment prod: pr.teamReviewers is not supported by the gitlab SCM provider (docs/scm-providers.md#pr-controls)", result.Message)
		assert.Empty(t, calls(), "no MR is opened")
	})
}

// TestOpenPRControls_Forgejo: reviewers and teams are requested, assignees
// added to the PR's, and the PR is scheduled to merge when its checks
// succeed with the method and message. Covers SCM-PRCTL-FJ-01.
func TestOpenPRControls_Forgejo(t *testing.T) {
	defer steps.SetAutoMergeRetryDelays([]time.Duration{0})()
	const repo = "/api/v1/repos/acme/web"
	srv, calls := routedAPI(t, map[string][]apiReply{
		"POST " + repo + "/pulls":                       {{201, `{"number":4,"html_url":"https://forgejo.example/acme/web/pulls/4"}`}},
		"GET " + repo + "/labels":                       {{200, `[{"id":1,"name":"kardinal"},{"id":2,"name":"kardinal/promotion"}]`}},
		"POST " + repo + "/labels":                      {{201, `{"id":3}`}},
		"POST " + repo + "/issues/4/labels":             {{200, `[]`}},
		"POST " + repo + "/pulls/4/requested_reviewers": {{201, `[]`}},
		"GET " + repo + "/issues/4":                     {{200, `{"assignees":[{"login":"bob"}]}`}},
		"PATCH " + repo + "/issues/4":                   {{201, `{}`}},
		"POST " + repo + "/pulls/4/merge":               {{200, ``}},
	})
	p, err := scm.NewProvider("forgejo", "fj", srv.URL, "")
	require.NoError(t, err)

	result, err := runOpenPR(t, controlsState(t, "https://forgejo.example/acme/web.git", p, fullPRConfig("rebase")))
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status, result.Message)
	assert.Equal(t, "enabled", result.Outputs[parentsteps.OutputPRAutoMerge])

	got := calls()
	assert.Equal(t, []string{"POST " + repo + "/pulls", "GET " + repo + "/labels?limit=50&page=1", "POST " + repo + "/labels",
		"POST " + repo + "/issues/4/labels", "POST " + repo + "/pulls/4/requested_reviewers", "GET " + repo + "/issues/4",
		"PATCH " + repo + "/issues/4", "POST " + repo + "/pulls/4/merge"}, routes(got))
	assert.Equal(t, "env/prod", got[2].Body["name"])
	assert.Equal(t, []interface{}{float64(1), float64(2), float64(3)}, got[3].Body["labels"])
	assert.Equal(t, map[string]interface{}{"reviewers": []interface{}{"alice"}, "team_reviewers": []interface{}{"platform"}}, got[4].Body)
	assert.Equal(t, []interface{}{"bob", "octocat"}, got[6].Body["assignees"], "the PR's assignee is kept")
	assert.Equal(t, map[string]interface{}{"Do": "rebase", "merge_when_checks_succeed": true, "delete_branch_after_merge": false,
		"MergeTitleField": "deploy 1.29.0 to prod (#4)", "MergeMessageField": "Bundle nginx-demo-v1-29-0"}, got[7].Body)
}

// TestOpenPRControls_Bitbucket: reviewers are added to the PR's by UUID or
// account ID with its title; labels, assignees, team reviewers and
// auto-merge are refused before a PR is opened. Covers SCM-PRCTL-BB-01.
func TestOpenPRControls_Bitbucket(t *testing.T) {
	const pr = "/2.0/repositories/acme/web/pullrequests/7"
	srv, calls := routedAPI(t, map[string][]apiReply{
		"POST /2.0/repositories/acme/web/pullrequests": {{201, `{"id":7,"links":{"html":{"href":"https://bitbucket.org/acme/web/pull-requests/7"}}}`}},
		"GET " + pr: {{200, `{"title":"deploy","reviewers":[{"uuid":"{b0b}","account_id":"557058:b0b"}]}`}},
		"PUT " + pr: {{200, `{}`}},
	})
	p, err := scm.NewProvider("bitbucket", "bb", srv.URL, "")
	require.NoError(t, err)
	result, err := runOpenPR(t, controlsState(t, "https://bitbucket.org/acme/web.git", p,
		&v1alpha1.PRConfig{Reviewers: []string{"{a11ce}", "557058:0c7"}}))
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status, result.Message)
	got := calls()
	assert.Equal(t, []string{"POST /2.0/repositories/acme/web/pullrequests", "GET " + pr, "PUT " + pr}, routes(got))
	assert.Equal(t, map[string]interface{}{"title": "deploy", "reviewers": []interface{}{
		map[string]interface{}{"uuid": "{b0b}"}, map[string]interface{}{"uuid": "{a11ce}"}, map[string]interface{}{"account_id": "557058:0c7"},
	}}, got[2].Body)

	for name, cfg := range map[string]*v1alpha1.PRConfig{
		"labels":        {Labels: []string{"x"}},
		"assignees":     {Assignees: []string{"x"}},
		"teamReviewers": {TeamReviewers: []string{"x"}},
		"merge.auto":    {Merge: &v1alpha1.PRMergeConfig{Auto: true}},
	} {
		t.Run(name+" refused", func(t *testing.T) {
			srv, calls := routedAPI(t, nil)
			p, err := scm.NewProvider("bitbucket", "bb", srv.URL, "")
			require.NoError(t, err)
			result, err := runOpenPR(t, controlsState(t, "https://bitbucket.org/acme/web.git", p, cfg))
			assert.ErrorIs(t, err, parentsteps.ErrPermanent)
			assert.Contains(t, result.Message, "pr."+name+" is not supported by the bitbucket SCM provider")
			assert.Empty(t, calls())
		})
	}
}

// TestOpenPRControls_AzureDevOps: reviewers and team reviewers are added by
// identity ID, and auto-complete is set by the PR's creator with the merge
// strategy and message. Covers SCM-PRCTL-ADO-01.
func TestOpenPRControls_AzureDevOps(t *testing.T) {
	defer steps.SetAutoMergeRetryDelays([]time.Duration{0})()
	const repo = "/contoso/Web/_apis/git/repositories/web"
	srv, calls := routedAPI(t, map[string][]apiReply{
		"POST " + repo + "/pullrequests":                      {{201, `{"pullRequestId":12,"repository":{"webUrl":"https://dev.azure.com/contoso/Web/_git/web"}}`}},
		"POST " + repo + "/pullRequests/12/labels":            {{200, `{}`}},
		"PUT " + repo + "/pullRequests/12/reviewers/alice-id": {{200, `{}`}},
		"PUT " + repo + "/pullRequests/12/reviewers/team-id":  {{200, `{}`}},
		"GET " + repo + "/pullrequests/12":                    {{200, `{"createdBy":{"id":"creator-id"}}`}},
		"PATCH " + repo + "/pullrequests/12":                  {{200, `{}`}},
	})
	p, err := scm.NewProvider("azuredevops", "pat", srv.URL, "")
	require.NoError(t, err)
	cfg := fullPRConfig("merge")
	cfg.Assignees = nil
	cfg.Reviewers, cfg.TeamReviewers = []string{"alice-id"}, []string{"team-id"}

	result, err := runOpenPR(t, controlsState(t, "https://dev.azure.com/contoso/Web/_git/web", p, cfg))
	require.NoError(t, err)
	assert.Equal(t, parentsteps.StepSuccess, result.Status, result.Message)
	assert.Equal(t, "enabled", result.Outputs[parentsteps.OutputPRAutoMerge])
	got := calls()
	assert.Equal(t, []string{"POST " + repo + "/pullrequests?api-version=7.1",
		"POST " + repo + "/pullRequests/12/labels?api-version=7.1", "POST " + repo + "/pullRequests/12/labels?api-version=7.1",
		"POST " + repo + "/pullRequests/12/labels?api-version=7.1",
		"PUT " + repo + "/pullRequests/12/reviewers/alice-id?api-version=7.1", "PUT " + repo + "/pullRequests/12/reviewers/team-id?api-version=7.1",
		"GET " + repo + "/pullrequests/12?api-version=7.1", "PATCH " + repo + "/pullrequests/12?api-version=7.1"}, routes(got))
	assert.Equal(t, map[string]interface{}{
		"autoCompleteSetBy": map[string]interface{}{"id": "creator-id"},
		"completionOptions": map[string]interface{}{"mergeStrategy": "noFastForward", "deleteSourceBranch": false,
			"mergeCommitMessage": "deploy 1.29.0 to prod (#12)\n\nBundle nginx-demo-v1-29-0"},
	}, got[7].Body)
}

// TestOpenPRControls_Failures: a template that does not render fails the
// step before the PR; a control the SCM refuses after the PR is open does
// not fail the step and is kept in prControlsError, the others still
// applied; a permanent auto-merge error is not retried; a re-run with the PR
// recorded sends nothing. Covers SCM-PRCTL-ERR-01.
func TestOpenPRControls_Failures(t *testing.T) {
	defer steps.SetAutoMergeRetryDelays([]time.Duration{0, 0, 0})()
	const repo = "/api/v1/repos/acme/web"
	opened := apiReply{201, `{"number":4,"html_url":"https://forgejo.example/acme/web/pulls/4"}`}

	t.Run("template error", func(t *testing.T) {
		srv, calls := routedAPI(t, nil)
		p, err := scm.NewProvider("forgejo", "fj", srv.URL, "")
		require.NoError(t, err)
		result, err := runOpenPR(t, controlsState(t, "https://forgejo.example/acme/web.git", p,
			&v1alpha1.PRConfig{TitleTemplate: "{{ .Bundle.Nope }}"}))
		assert.ErrorIs(t, err, parentsteps.ErrPermanent)
		assert.Equal(t, parentsteps.StepFailed, result.Status)
		assert.Contains(t, result.Message, "environment prod: render PR: pr.titleTemplate:")
		assert.Empty(t, calls())
	})

	t.Run("controls refused after the PR", func(t *testing.T) {
		srv, calls := routedAPI(t, map[string][]apiReply{
			"POST " + repo + "/pulls":                       {opened},
			"GET " + repo + "/labels":                       {{200, `[{"id":1,"name":"kardinal"},{"id":2,"name":"kardinal/promotion"}]`}},
			"POST " + repo + "/issues/4/labels":             {{200, `[]`}},
			"POST " + repo + "/pulls/4/requested_reviewers": {{422, `{"message":"reviewer is not a collaborator"}`}},
			"GET " + repo + "/issues/4":                     {{200, `{"assignees":[]}`}},
			"PATCH " + repo + "/issues/4":                   {{201, `{}`}},
			"POST " + repo + "/pulls/4/merge":               {{403, `{"message":"user does not have permission"}`}},
		})
		p, err := scm.NewProvider("forgejo", "fj", srv.URL, "")
		require.NoError(t, err)
		state := controlsState(t, "https://forgejo.example/acme/web.git", p, &v1alpha1.PRConfig{
			Reviewers: []string{"alice"}, Assignees: []string{"octocat"}, Merge: &v1alpha1.PRMergeConfig{Auto: true}})
		result, err := runOpenPR(t, state)
		require.NoError(t, err, "the PR is open")
		assert.Equal(t, parentsteps.StepSuccess, result.Status)
		assert.Equal(t, "4", result.Outputs["prNumber"])
		e := result.Outputs[parentsteps.OutputPRControlsError]
		assert.Contains(t, e, "reviewers: request reviewers on PR acme/web#4")
		assert.Contains(t, e, "reviewer is not a collaborator")
		assert.Contains(t, e, "auto-merge: enable auto-merge on PR acme/web#4")
		assert.NotContains(t, e, "assignees")
		assert.Contains(t, result.Message, "(PR controls failed: ")
		assert.Empty(t, result.Outputs[parentsteps.OutputPRAutoMerge])
		merges := 0
		for _, r := range routes(calls()) {
			if r == "POST "+repo+"/pulls/4/merge" {
				merges++
			}
		}
		assert.Equal(t, 1, merges, "a 403 is permanent and not retried")
		assert.Contains(t, routes(calls()), "PATCH "+repo+"/issues/4", "the assignees are applied after the reviewers failed")

		// Re-run with the PR recorded: nothing is sent again.
		n := len(calls())
		state.Outputs = result.Outputs
		again, err := runOpenPR(t, state)
		require.NoError(t, err)
		assert.Equal(t, parentsteps.StepSuccess, again.Status)
		assert.Len(t, calls(), n)
	})

	t.Run("auto-merge retries run out", func(t *testing.T) {
		srv, calls := routedAPI(t, map[string][]apiReply{
			"POST " + repo + "/pulls":           {opened},
			"GET " + repo + "/labels":           {{200, `[{"id":1,"name":"kardinal"},{"id":2,"name":"kardinal/promotion"}]`}},
			"POST " + repo + "/issues/4/labels": {{200, `[]`}},
			"POST " + repo + "/pulls/4/merge":   {{405, `{"message":"Please try again later"}`}},
		})
		p, err := scm.NewProvider("forgejo", "fj", srv.URL, "")
		require.NoError(t, err)
		result, err := runOpenPR(t, controlsState(t, "https://forgejo.example/acme/web.git", p,
			&v1alpha1.PRConfig{Merge: &v1alpha1.PRMergeConfig{Auto: true}}))
		require.NoError(t, err)
		assert.Contains(t, result.Outputs[parentsteps.OutputPRControlsError], "Please try again later")
		merges := 0
		for _, r := range routes(calls()) {
			if r == "POST "+repo+"/pulls/4/merge" {
				merges++
			}
		}
		assert.Equal(t, 4, merges, "one attempt and three retries")
	})

	t.Run("a later PR without errors clears the output", func(t *testing.T) {
		srv, _ := routedAPI(t, map[string][]apiReply{
			"POST " + repo + "/pulls":           {opened},
			"GET " + repo + "/labels":           {{200, `[{"id":1,"name":"kardinal"},{"id":2,"name":"kardinal/promotion"}]`}},
			"POST " + repo + "/issues/4/labels": {{200, `[]`}},
		})
		p, err := scm.NewProvider("forgejo", "fj", srv.URL, "")
		require.NoError(t, err)
		state := controlsState(t, "https://forgejo.example/acme/web.git", p, &v1alpha1.PRConfig{})
		state.Outputs[parentsteps.OutputPRControlsError] = "old"
		result, err := runOpenPR(t, state)
		require.NoError(t, err)
		v, ok := result.Outputs[parentsteps.OutputPRControlsError]
		assert.True(t, ok)
		assert.Empty(t, v)
	})
}
