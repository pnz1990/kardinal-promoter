// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// apiCall is one request the fake SCM API received.
type apiCall struct {
	// Route is the method and the escaped path, as in "PUT /api/v4/projects/acme%2Fweb-app/merge_requests/7".
	Route string
	Body  map[string]interface{}
}

// apiResponse is the fake SCM API's answer to a route.
type apiResponse struct {
	status int
	body   string
}

// fakeAPI answers each route with its response, every other request with
// 404, and records the requests. Every request must carry the token in
// header tokenHeader as want.
func fakeAPI(t *testing.T, tokenHeader, want string, routes map[string]apiResponse) (*httptest.Server, func() []apiCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []apiCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := apiCall{Route: r.Method + " " + r.URL.EscapedPath()}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			assert.NoError(t, json.Unmarshal(raw, &c.Body), c.Route)
		}
		assert.Equal(t, want, r.Header.Get(tokenHeader), "%s: %s", c.Route, tokenHeader)
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		resp, ok := routes[c.Route]
		if !ok {
			resp = apiResponse{http.StatusNotFound, `{"message":"Not Found"}`}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.status)
		_, _ = io.WriteString(w, resp.body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []apiCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]apiCall(nil), calls...)
	}
}

// labelsRefused runs the open-pr and wait-for-merge steps of a prod
// PromotionStep of a Pipeline on gitURL with provider p, whose fake API opens
// PR prNumber, refuses to label it, and reports it open on route status. The
// step must wait for the merge with the failure, wantErr, in its message and
// in status.outputs.prLabelsError, on the reconcile that opened the PR and on
// the next one, and after the label request only read the PR's status: the
// PR is left open. It returns the first two requests, the create and the
// label request.
func labelsRefused(t *testing.T, gitURL string, p scm.SCMProvider, calls func() []apiCall,
	status, prURL, prNumber, wantErr string) []apiCall {
	t.Helper()
	pl := makePipeline("p")
	pl.Spec.Git.URL = gitURL
	ps := asPromoting(labelled(makeStep("step", "p", "b1", "prod")), pl)
	ps.Status.CurrentStepIndex = 4 // open-pr, then wait-for-merge
	ps.Spec.PRStatusRef = "prs"
	c := newClient(t, ps, openPRStatus("prs", "", 0), pl, makeBundle("b1", "p"))
	r := &promotionstep.Reconciler{Client: c, SCM: p, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}

	want := "PR #" + prNumber + " is open, waiting for merge; adding labels failed: " + wantErr
	reconcileStep(t, r, "step")
	got := getStep(t, c, "step")
	assert.Equal(t, "WaitingForMerge", got.Status.State)
	assert.Equal(t, want, got.Status.Message)
	assert.Equal(t, wantErr, got.Status.Outputs["prLabelsError"])
	assert.Equal(t, prURL, got.Status.Outputs["prURL"])
	assert.Equal(t, prNumber, got.Status.Outputs["prNumber"])

	reconcileStep(t, r, "step")
	got = getStep(t, c, "step")
	assert.Equal(t, "WaitingForMerge", got.Status.State)
	assert.Equal(t, want, got.Status.Message, "the next reconcile keeps the failure in the message")
	assert.Equal(t, wantErr, got.Status.Outputs["prLabelsError"])

	all := calls()
	require.Greater(t, len(all), 2, "the PR create, the label request and the status reads: %+v", all)
	for _, c := range all[2:] {
		assert.Equal(t, status, c.Route, "after the label request the step only reads the PR status")
	}
	return all[:2]
}

// TestLabelsRefused_GitHub runs open-pr against a fake GitHub API that opens
// the PR and answers the label request (POST /repos/{repo}/issues/{n}/labels)
// with 403, as GitHub does for a token without Issues or Pull requests write.
// The step waits for the merge, the PR stays open (the step only reads its
// status), and the WaitingForMerge message and status.outputs.prLabelsError
// name the refused request. Covers SCM-GH-11.
func TestLabelsRefused_GitHub(t *testing.T) {
	const (
		labels  = "POST /repos/acme/web-app/issues/7/labels"
		status  = "GET /repos/acme/web-app/pulls/7"
		refused = `{"message":"Resource not accessible by personal access token",` +
			`"documentation_url":"https://docs.github.com/rest/issues/labels#add-labels-to-an-issue","status":"403"}`
	)
	srv, calls := fakeAPI(t, "Authorization", "Bearer gh-token", map[string]apiResponse{
		"POST /repos/acme/web-app/pulls": {http.StatusCreated,
			`{"number":7,"state":"open","html_url":"https://github.com/acme/web-app/pull/7"}`},
		labels: {http.StatusForbidden, refused},
		status: {http.StatusOK, `{"number":7,"state":"open","merged":false}`},
	})
	p, err := scm.NewProvider("github", "gh-token", srv.URL, "")
	require.NoError(t, err)

	got := labelsRefused(t, "https://github.com/acme/web-app.git", p, calls, status,
		"https://github.com/acme/web-app/pull/7", "7",
		"add labels to PR acme/web-app#7: GitHub API POST /repos/acme/web-app/issues/7/labels: status 403: "+refused)
	assert.Equal(t, "POST /repos/acme/web-app/pulls", got[0].Route)
	assert.Equal(t, "kardinal/37a8eec1/b1/prod", got[0].Body["head"])
	assert.Equal(t, labels, got[1].Route)
	assert.Equal(t, map[string]interface{}{"labels": []interface{}{"kardinal", "kardinal/promotion"}}, got[1].Body)
}

// TestLabelsRefused_GitLab runs open-pr against a fake GitLab API that opens
// the MR and answers the label request (PUT .../merge_requests/{iid} with
// add_labels) with 403. The step waits for the merge, the MR stays open (the
// step only reads its status; it sends no state_event close), and the
// WaitingForMerge message and status.outputs.prLabelsError name the refused
// request. Covers SCM-GL-13.
func TestLabelsRefused_GitLab(t *testing.T) {
	const (
		mr      = "PUT /api/v4/projects/acme%2Fweb-app/merge_requests/7"
		status  = "GET /api/v4/projects/acme%2Fweb-app/merge_requests/7"
		refused = `{"message":"403 Forbidden"}`
	)
	srv, calls := fakeAPI(t, "PRIVATE-TOKEN", "gl-token", map[string]apiResponse{
		"POST /api/v4/projects/acme%2Fweb-app/merge_requests": {http.StatusCreated,
			`{"id":1207,"iid":7,"state":"opened","web_url":"https://gitlab.example.com/acme/web-app/-/merge_requests/7"}`},
		mr:     {http.StatusForbidden, refused},
		status: {http.StatusOK, `{"id":1207,"iid":7,"state":"opened"}`},
	})
	p, err := scm.NewProvider("gitlab", "gl-token", srv.URL, "")
	require.NoError(t, err)

	got := labelsRefused(t, "https://gitlab.example.com/acme/web-app.git", p, calls, status,
		"https://gitlab.example.com/acme/web-app/-/merge_requests/7", "7",
		"add labels to MR acme/web-app!7: GitLab API PUT /api/v4/projects/acme%2Fweb-app/merge_requests/7: status 403: "+refused)
	assert.Equal(t, "POST /api/v4/projects/acme%2Fweb-app/merge_requests", got[0].Route)
	assert.Equal(t, "kardinal/37a8eec1/b1/prod", got[0].Body["source_branch"])
	assert.Equal(t, mr, got[1].Route)
	assert.Equal(t, map[string]interface{}{"add_labels": "kardinal,kardinal/promotion"}, got[1].Body)
}
