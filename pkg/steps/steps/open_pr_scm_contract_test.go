// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// scmRequest is one request the fake SCM API received.
type scmRequest struct {
	Method, Path, Auth string
	Body               map[string]interface{}
}

// fakeSCMAPI answers every request with handle and records it.
func fakeSCMAPI(t *testing.T, handle func(r *http.Request) (int, string)) (*httptest.Server, func() []scmRequest) {
	t.Helper()
	var mu sync.Mutex
	var reqs []scmRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		req := scmRequest{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization")}
		if len(raw) > 0 {
			assert.NoError(t, json.Unmarshal(raw, &req.Body), "%s %s body", r.Method, r.URL.Path)
		}
		mu.Lock()
		reqs = append(reqs, req)
		mu.Unlock()
		status, body := handle(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []scmRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]scmRequest(nil), reqs...)
	}
}

// openPRState is the open-pr step state for a Pipeline whose spec.git.url is
// gitURL, with provider p. rollbackOf != "" makes it a rollback that deploys
// the restored image, nginx 1.28.0.
func openPRState(t *testing.T, gitURL string, p scm.SCMProvider, rollbackOf string) *parentsteps.StepState {
	t.Helper()
	state := makeState(t, &mockGitClient{}, nil)
	state.SCM = p
	state.Pipeline.Git.URL = gitURL
	state.Git.URL = gitURL
	if rollbackOf != "" {
		state.Bundle.Provenance = &v1alpha1.BundleProvenance{RollbackOf: rollbackOf, Author: "ci"}
		state.Bundle.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/nginx/nginx", Tag: "1.28.0"}}
	}
	return state
}

// TestOpenPRStep_BitbucketCloud runs the open-pr step against a fake
// Bitbucket Cloud API: the PR is opened on the workspace/repo of spec.git.url
// with the access token as a Bearer token, and no label request is sent
// because Bitbucket Cloud has no PR labels, so a rollback PR is told apart
// only by its "[kardinal] Rollback" title. Covers SCM-BB-01, SCM-BB-02.
func TestOpenPRStep_BitbucketCloud(t *testing.T) {
	const path = "/2.0/repositories/acme/web-app/pullrequests"
	tests := []struct {
		name, rollbackOf, wantTitle string
	}{
		{name: "promotion", wantTitle: "[kardinal] Promote nginx-demo-v1-29-0 to prod"},
		{name: "rollback", rollbackOf: "nginx-demo-v1-28-0",
			wantTitle: "[kardinal] Rollback prod to nginx-demo-v1-29-0 (restores 1.28.0)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, requests := fakeSCMAPI(t, func(r *http.Request) (int, string) {
				if r.Method != http.MethodPost || r.URL.Path != path {
					return http.StatusNotFound, `{"type":"error","error":{"message":"Resource not found"}}`
				}
				return http.StatusCreated, `{"type":"pullrequest","id":7,"state":"OPEN",` +
					`"links":{"html":{"href":"https://bitbucket.org/acme/web-app/pull-requests/7"}}}`
			})
			p, err := scm.NewProvider("bitbucket", "bb-access-token", srv.URL, "")
			require.NoError(t, err)
			step, err := parentsteps.Lookup("open-pr")
			require.NoError(t, err)

			result, err := step.Execute(context.Background(),
				openPRState(t, "https://bitbucket.org/acme/web-app.git", p, tt.rollbackOf))
			require.NoError(t, err)
			assert.Equal(t, parentsteps.StepSuccess, result.Status, result.Message)
			assert.Equal(t, "https://bitbucket.org/acme/web-app/pull-requests/7", result.Outputs["prURL"])
			assert.Equal(t, "7", result.Outputs["prNumber"])
			assert.NotContains(t, result.Message, "adding labels failed")

			reqs := requests()
			require.Len(t, reqs, 1, "one PR create and no label request")
			assert.Equal(t, http.MethodPost, reqs[0].Method)
			assert.Equal(t, path, reqs[0].Path)
			assert.Equal(t, "Bearer bb-access-token", reqs[0].Auth)
			assert.Equal(t, tt.wantTitle, reqs[0].Body["title"])
			assert.Equal(t, map[string]interface{}{"branch": map[string]interface{}{"name": "kardinal/nginx-demo-v1-29-0/prod"}}, reqs[0].Body["source"])
			assert.Equal(t, map[string]interface{}{"branch": map[string]interface{}{"name": "main"}}, reqs[0].Body["destination"])
			assert.Contains(t, reqs[0].Body["description"], "nginx-demo-v1-29-0", "the PR body is the promotion evidence")
		})
	}

	t.Run("token rejected", func(t *testing.T) {
		srv, requests := fakeSCMAPI(t, func(*http.Request) (int, string) {
			return http.StatusUnauthorized, `{"type":"error","error":{"message":"Token is invalid, expired, or not supported for this endpoint."}}`
		})
		p, err := scm.NewProvider("bitbucket", "revoked", srv.URL, "")
		require.NoError(t, err)
		step, err := parentsteps.Lookup("open-pr")
		require.NoError(t, err)

		result, err := step.Execute(context.Background(), openPRState(t, "https://bitbucket.org/acme/web-app.git", p, ""))
		require.Error(t, err)
		assert.Equal(t, parentsteps.StepFailed, result.Status)
		assert.Contains(t, result.Message, "status 401")
		assert.Len(t, requests(), 1)
	})
}

// TestOpenPRStep_AzureDevOps runs the open-pr step against a fake Azure
// DevOps API: the PR is opened on the org/project/repo of a dev.azure.com
// spec.git.url with the PAT as Basic auth, and the kardinal labels are then
// applied as PR tags, kardinal/rollback only on a rollback.
// Covers SCM-ADO-01, SCM-ADO-02.
func TestOpenPRStep_AzureDevOps(t *testing.T) {
	const (
		prs    = "/contoso/Web/_apis/git/repositories/web-app/pullrequests"
		labels = "/contoso/Web/_apis/git/repositories/web-app/pullRequests/12/labels"
		prURL  = "https://dev.azure.com/contoso/Web/_git/web-app/pullrequest/12"
	)
	tests := []struct {
		name, rollbackOf, wantTitle string
		wantLabels                  []string
	}{
		{name: "promotion", wantTitle: "[kardinal] Promote nginx-demo-v1-29-0 to prod",
			wantLabels: []string{"kardinal", "kardinal/promotion"}},
		{name: "rollback", rollbackOf: "nginx-demo-v1-28-0",
			wantTitle:  "[kardinal] Rollback prod to nginx-demo-v1-29-0 (restores 1.28.0)",
			wantLabels: []string{"kardinal", "kardinal/promotion", "kardinal/rollback"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, requests := fakeSCMAPI(t, func(r *http.Request) (int, string) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == prs:
					return http.StatusCreated, `{"pullRequestId":12,"status":"active",` +
						`"repository":{"name":"web-app","project":{"name":"Web"},"webUrl":"https://dev.azure.com/contoso/Web/_git/web-app"}}`
				case r.Method == http.MethodPost && r.URL.Path == labels:
					return http.StatusOK, `{"active":true,"id":"b4f5d1f0-8c1b-4f8e-9a7b-2f3c4d5e6f70","name":"kardinal"}`
				}
				return http.StatusNotFound, `{"message":"not found"}`
			})
			p, err := scm.NewProvider("azuredevops", "ado-pat", srv.URL, "")
			require.NoError(t, err)
			step, err := parentsteps.Lookup("open-pr")
			require.NoError(t, err)

			result, err := step.Execute(context.Background(),
				openPRState(t, "https://dev.azure.com/contoso/Web/_git/web-app", p, tt.rollbackOf))
			require.NoError(t, err)
			assert.Equal(t, parentsteps.StepSuccess, result.Status, result.Message)
			assert.Equal(t, prURL, result.Outputs["prURL"])
			assert.Equal(t, "12", result.Outputs["prNumber"])
			assert.NotContains(t, result.Message, "adding labels failed")

			reqs := requests()
			require.Len(t, reqs, 1+len(tt.wantLabels))
			assert.Equal(t, prs, reqs[0].Path)
			assert.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte(":ado-pat")), reqs[0].Auth)
			assert.Equal(t, tt.wantTitle, reqs[0].Body["title"])
			assert.Equal(t, "refs/heads/kardinal/nginx-demo-v1-29-0/prod", reqs[0].Body["sourceRefName"])
			assert.Equal(t, "refs/heads/main", reqs[0].Body["targetRefName"])
			var got []string
			for _, r := range reqs[1:] {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, labels, r.Path)
				assert.Equal(t, reqs[0].Auth, r.Auth)
				got = append(got, r.Body["name"].(string))
			}
			assert.Equal(t, tt.wantLabels, got)
		})
	}

	t.Run("tags refused", func(t *testing.T) {
		srv, _ := fakeSCMAPI(t, func(r *http.Request) (int, string) {
			if r.URL.Path == prs {
				return http.StatusCreated, `{"pullRequestId":12,"repository":{"webUrl":"https://dev.azure.com/contoso/Web/_git/web-app"}}`
			}
			return http.StatusForbidden, `{"message":"TF401027: You need the Git 'PullRequestContribute' permission to perform this action.","typeKey":"GitNeedsPermissionException"}`
		})
		p, err := scm.NewProvider("azuredevops", "ado-pat", srv.URL, "")
		require.NoError(t, err)
		step, err := parentsteps.Lookup("open-pr")
		require.NoError(t, err)

		result, err := step.Execute(context.Background(), openPRState(t, "https://dev.azure.com/contoso/Web/_git/web-app", p, ""))
		require.NoError(t, err, "the PR is open; missing tags do not fail the step")
		assert.Equal(t, parentsteps.StepSuccess, result.Status)
		assert.Equal(t, prURL, result.Outputs["prURL"])
		assert.Contains(t, result.Message, "adding labels failed")
		assert.Contains(t, result.Message, "TF401027")
	})
}
