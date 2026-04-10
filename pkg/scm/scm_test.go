// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package scm_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

func TestGitHubProvider_OpenPR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/owner/repo/pulls", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.Header.Get("Authorization"), "Bearer test-token")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"number":   42,
			"html_url": "https://github.com/owner/repo/pull/42",
		})
	}))
	defer server.Close()

	p := scm.NewGitHubProvider("test-token", server.URL, "")
	url, num, err := p.OpenPR(context.Background(), "owner/repo", "Test PR", "body", "feature", "main")
	require.NoError(t, err)
	assert.Equal(t, 42, num)
	assert.Equal(t, "https://github.com/owner/repo/pull/42", url)
}

func TestGitHubProvider_ClosePR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/owner/repo/pulls/42", r.URL.Path)
		assert.Equal(t, http.MethodPatch, r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := scm.NewGitHubProvider("test-token", server.URL, "")
	require.NoError(t, p.ClosePR(context.Background(), "owner/repo", 42))
}

func TestGitHubProvider_CommentOnPR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/owner/repo/issues/42/comments", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := scm.NewGitHubProvider("test-token", server.URL, "")
	require.NoError(t, p.CommentOnPR(context.Background(), "owner/repo", 42, "hello"))
}

func TestGitHubProvider_GetPRStatus(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse map[string]interface{}
		wantMerged     bool
		wantOpen       bool
	}{
		{
			name:           "open PR",
			serverResponse: map[string]interface{}{"state": "open", "merged": false},
			wantMerged:     false,
			wantOpen:       true,
		},
		{
			name:           "merged PR",
			serverResponse: map[string]interface{}{"state": "closed", "merged": true},
			wantMerged:     true,
			wantOpen:       false,
		},
		{
			name:           "closed unmerged PR",
			serverResponse: map[string]interface{}{"state": "closed", "merged": false},
			wantMerged:     false,
			wantOpen:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tc.serverResponse)
			}))
			defer server.Close()

			p := scm.NewGitHubProvider("test-token", server.URL, "")
			merged, open, err := p.GetPRStatus(context.Background(), "owner/repo", 42)
			require.NoError(t, err)
			assert.Equal(t, tc.wantMerged, merged)
			assert.Equal(t, tc.wantOpen, open)
		})
	}
}

func TestGitHubProvider_ParseWebhookEvent(t *testing.T) {
	secret := "my-webhook-secret"
	payload := []byte(`{
		"action": "closed",
		"pull_request": {"number": 42, "merged": true},
		"repository": {"full_name": "owner/repo"}
	}`)

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	p := scm.NewGitHubProvider("token", "", secret)
	event, err := p.ParseWebhookEvent(payload, sig)
	require.NoError(t, err)
	assert.Equal(t, "pull_request", event.EventType)
	assert.Equal(t, 42, event.PRNumber)
	assert.True(t, event.Merged)
	assert.Equal(t, "owner/repo", event.RepoFullName)
	assert.Equal(t, "closed", event.Action)
}

func TestGitHubProvider_ParseWebhookEvent_InvalidSignature(t *testing.T) {
	p := scm.NewGitHubProvider("token", "", "correct-secret")
	payload := []byte(`{"action": "closed"}`)
	_, err := p.ParseWebhookEvent(payload, "sha256=badhex")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")
}

func TestGitHubProvider_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message": "Validation Failed"}`))
	}))
	defer server.Close()

	p := scm.NewGitHubProvider("test-token", server.URL, "")
	_, _, err := p.OpenPR(context.Background(), "owner/repo", "Test", "body", "head", "base")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "422")
}

func TestRenderPRBody(t *testing.T) {
	data := scm.PRBody{
		PipelineName: "nginx-demo",
		Environment:  "prod",
		BundleName:   "nginx-demo-v1-29-0",
		Bundle: v1alpha1.BundleSpec{
			Type: "image",
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/nginx/nginx", Tag: "1.29.0"},
			},
			Provenance: &v1alpha1.BundleProvenance{
				CommitSHA: "abc123",
				Author:    "ci-bot",
				CIRunURL:  "https://github.com/runs/1",
			},
		},
		GateResults: []v1alpha1.GateResult{
			{GateName: "no-weekend-deploys", Result: "pass", Reason: "weekday"},
		},
		UpstreamEnvironments: []v1alpha1.EnvironmentStatus{
			{Name: "test", Phase: "Verified"},
			{Name: "uat", Phase: "Verified"},
		},
	}

	body, err := scm.RenderPRBody(data)
	require.NoError(t, err)

	assert.Contains(t, body, "nginx-demo-v1-29-0")
	assert.Contains(t, body, "ghcr.io/nginx/nginx:1.29.0")
	assert.Contains(t, body, "no-weekend-deploys")
	assert.Contains(t, body, "pass")
	assert.Contains(t, body, "Verified")
	// All three tables present
	assert.True(t, strings.Contains(body, "Artifact Provenance"), "missing provenance table")
	assert.True(t, strings.Contains(body, "Policy Gate Compliance"), "missing gate table")
	assert.True(t, strings.Contains(body, "Upstream Verification"), "missing upstream table")
}

func TestInjectToken(t *testing.T) {
	// Test injectToken indirectly: Push should not fail with empty remote
	// (we test the URL manipulation logic via exported function if available,
	// otherwise just test that Push returns an error about git not finding the dir,
	// not about token injection).
	c := scm.NewExecGitClient()
	err := c.Push(context.Background(), "/nonexistent", "origin", "main", "tok")
	require.Error(t, err)
	// Error should mention git, not URL injection
	assert.NotContains(t, err.Error(), "unsupported remote URL")
}
