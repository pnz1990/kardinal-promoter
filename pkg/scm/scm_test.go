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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	// New format: repo and tag are in separate columns
	assert.Contains(t, body, "ghcr.io/nginx/nginx")
	assert.Contains(t, body, "1.29.0")
	assert.Contains(t, body, "no-weekend-deploys")
	assert.Contains(t, body, "pass")
	// Upstream environments appear by name
	assert.Contains(t, body, "test")
	assert.Contains(t, body, "uat")
	// All three tables present
	assert.True(t, strings.Contains(body, "Artifact Provenance"), "missing provenance table")
	assert.True(t, strings.Contains(body, "Policy Gate Compliance"), "missing gate table")
	assert.True(t, strings.Contains(body, "Upstream Verification"), "missing upstream table")
}

func TestGitHubProvider_AddLabelsToPR(t *testing.T) {
	var capturedLabels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/owner/repo/issues/42/labels", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		var payload map[string][]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		capturedLabels = payload["labels"]
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	p := scm.NewGitHubProvider("test-token", server.URL, "")
	err := p.AddLabelsToPR(context.Background(), "owner/repo", 42, []string{"kardinal", "kardinal/promotion"})
	require.NoError(t, err)
	assert.Contains(t, capturedLabels, "kardinal")
	assert.Contains(t, capturedLabels, "kardinal/promotion")
}

func TestGitHubProvider_AddLabelsToPR_Empty(t *testing.T) {
	// No HTTP calls should be made for empty label list.
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	p := scm.NewGitHubProvider("test-token", server.URL, "")
	err := p.AddLabelsToPR(context.Background(), "owner/repo", 42, nil)
	require.NoError(t, err)
	assert.False(t, called, "no HTTP call for empty labels")
}

func TestGitHubProvider_EnsureLabels_CreatesIfMissing(t *testing.T) {
	var createdNames []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/owner/repo/labels", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		createdNames = append(createdNames, payload["name"])
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	labels := scm.DefaultKardinalLabels()
	p := scm.NewGitHubProvider("test-token", server.URL, "")
	err := p.EnsureLabels(context.Background(), "owner/repo", labels)
	require.NoError(t, err)
	assert.Len(t, createdNames, len(labels))
	assert.Contains(t, createdNames, "kardinal")
	assert.Contains(t, createdNames, "kardinal/promotion")
	assert.Contains(t, createdNames, "kardinal/rollback")
	assert.Contains(t, createdNames, "kardinal/emergency")
}

func TestGitHubProvider_EnsureLabels_AlreadyExists(t *testing.T) {
	// When GitHub returns 422 with already_exists, EnsureLabels should not return an error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"code":"already_exists"}]}`))
	}))
	defer server.Close()

	labels := []scm.Label{{Name: "kardinal", Color: "0075ca"}}
	p := scm.NewGitHubProvider("test-token", server.URL, "")
	err := p.EnsureLabels(context.Background(), "owner/repo", labels)
	require.NoError(t, err, "422 already_exists should not be an error")
}

func TestPRTemplate_GateComplianceWithNamespace(t *testing.T) {
	evalTime := metav1.NewTime(time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC))
	data := scm.PRBody{
		PipelineName: "nginx-demo",
		Environment:  "prod",
		BundleName:   "nginx-demo-v1-29-0",
		Bundle: v1alpha1.BundleSpec{
			Type: "image",
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/nginx/nginx", Tag: "1.29.0", Digest: "sha256:abc123"},
			},
			Provenance: &v1alpha1.BundleProvenance{
				CommitSHA: "abc123",
				Author:    "ci-bot",
				CIRunURL:  "https://github.com/runs/1",
			},
		},
		GateResults: []v1alpha1.GateResult{
			{GateName: "no-weekend-deploys", GateNamespace: "platform-policies", Result: "pass", Reason: "weekday", EvaluatedAt: evalTime},
		},
		UpstreamEnvironments: []v1alpha1.EnvironmentStatus{
			{Name: "test", Phase: "Verified"},
			{Name: "uat", Phase: "Verified"},
		},
	}

	body, err := scm.RenderPRBody(data)
	require.NoError(t, err)

	assert.Contains(t, body, "platform-policies", "gate namespace must appear in PR body")
	assert.Contains(t, body, "no-weekend-deploys")
	assert.Contains(t, body, "sha256:abc123", "digest must appear in provenance table")
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

// ─── GitLab SCM Provider Tests ────────────────────────────────────────────────

func TestGitLabProvider_OpenPR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GitLab API: POST /api/v4/projects/:id/merge_requests
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "merge_requests")
		assert.Equal(t, "glpat-test-token", r.Header.Get("PRIVATE-TOKEN"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"iid":     77,
			"web_url": "https://gitlab.com/owner/repo/-/merge_requests/77",
			"state":   "opened",
		})
	}))
	defer server.Close()

	p := scm.NewGitLabProvider("glpat-test-token", server.URL, "")
	url, num, err := p.OpenPR(context.Background(), "owner/repo", "Test MR", "body", "feature", "main")
	require.NoError(t, err)
	assert.Equal(t, 77, num)
	assert.Equal(t, "https://gitlab.com/owner/repo/-/merge_requests/77", url)
}

func TestGitLabProvider_OpenPR_AlreadyExists(t *testing.T) {
	// When a MR already exists for the source branch, OpenPR returns the existing MR.
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Method == http.MethodPost {
			// First call: return 409 Conflict to signal MR exists.
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":["Another open merge request already exists for this source branch"]}`))
			return
		}
		// Second call: GET list of open MRs.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"iid":           88,
				"web_url":       "https://gitlab.com/owner/repo/-/merge_requests/88",
				"state":         "opened",
				"source_branch": "feature",
			},
		})
	}))
	defer server.Close()

	p := scm.NewGitLabProvider("glpat-test-token", server.URL, "")
	url, num, err := p.OpenPR(context.Background(), "owner/repo", "Test MR", "body", "feature", "main")
	require.NoError(t, err)
	assert.Equal(t, 88, num)
	assert.Equal(t, "https://gitlab.com/owner/repo/-/merge_requests/88", url)
}

func TestGitLabProvider_ClosePR(t *testing.T) {
	var capturedMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		assert.Contains(t, r.URL.Path, "merge_requests/42")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"state":"closed"}`))
	}))
	defer server.Close()

	p := scm.NewGitLabProvider("glpat-test-token", server.URL, "")
	require.NoError(t, p.ClosePR(context.Background(), "owner/repo", 42))
	assert.Equal(t, http.MethodPut, capturedMethod)
}

func TestGitLabProvider_CommentOnPR(t *testing.T) {
	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "notes")
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		capturedBody = payload["body"]
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := scm.NewGitLabProvider("glpat-test-token", server.URL, "")
	require.NoError(t, p.CommentOnPR(context.Background(), "owner/repo", 42, "hello gitlab"))
	assert.Equal(t, "hello gitlab", capturedBody)
}

func TestGitLabProvider_GetPRStatus(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse map[string]interface{}
		wantMerged     bool
		wantOpen       bool
	}{
		{
			name:           "opened MR",
			serverResponse: map[string]interface{}{"state": "opened"},
			wantMerged:     false,
			wantOpen:       true,
		},
		{
			name:           "merged MR",
			serverResponse: map[string]interface{}{"state": "merged"},
			wantMerged:     true,
			wantOpen:       false,
		},
		{
			name:           "closed MR",
			serverResponse: map[string]interface{}{"state": "closed"},
			wantMerged:     false,
			wantOpen:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tc.serverResponse)
			}))
			defer server.Close()

			p := scm.NewGitLabProvider("glpat-test-token", server.URL, "")
			merged, open, err := p.GetPRStatus(context.Background(), "owner/repo", 42)
			require.NoError(t, err)
			assert.Equal(t, tc.wantMerged, merged)
			assert.Equal(t, tc.wantOpen, open)
		})
	}
}

func TestGitLabProvider_ParseWebhookEvent_ValidToken(t *testing.T) {
	secret := "my-gitlab-webhook-secret"
	payload := []byte(`{
		"object_kind": "merge_request",
		"object_attributes": {
			"iid": 42,
			"state": "merged",
			"action": "merge"
		},
		"project": {"path_with_namespace": "owner/repo"}
	}`)

	p := scm.NewGitLabProvider("token", "", secret)
	event, err := p.ParseWebhookEvent(payload, secret)
	require.NoError(t, err)
	assert.Equal(t, "merge_request", event.EventType)
	assert.Equal(t, 42, event.PRNumber)
	assert.True(t, event.Merged)
	assert.Equal(t, "owner/repo", event.RepoFullName)
	assert.Equal(t, "merge", event.Action)
}

func TestGitLabProvider_ParseWebhookEvent_InvalidToken(t *testing.T) {
	p := scm.NewGitLabProvider("token", "", "correct-secret")
	payload := []byte(`{"object_kind": "merge_request"}`)
	_, err := p.ParseWebhookEvent(payload, "wrong-secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token")
}

func TestGitLabProvider_ParseWebhookEvent_NoSecret(t *testing.T) {
	// When no secret is configured, any token passes.
	p := scm.NewGitLabProvider("token", "", "")
	payload := []byte(`{
		"object_kind": "merge_request",
		"object_attributes": {"iid": 5, "state": "opened", "action": "open"},
		"project": {"path_with_namespace": "g/r"}
	}`)
	event, err := p.ParseWebhookEvent(payload, "anything")
	require.NoError(t, err)
	assert.Equal(t, 5, event.PRNumber)
}

func TestGitLabProvider_AddLabelsToPR(t *testing.T) {
	var capturedLabels string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Contains(t, r.URL.Path, "merge_requests/42")
		var payload map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		capturedLabels = payload["labels"]
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := scm.NewGitLabProvider("glpat-test-token", server.URL, "")
	err := p.AddLabelsToPR(context.Background(), "owner/repo", 42, []string{"kardinal", "kardinal/promotion"})
	require.NoError(t, err)
	assert.Contains(t, capturedLabels, "kardinal")
}

func TestGitLabProvider_AddLabelsToPR_Empty(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	p := scm.NewGitLabProvider("glpat-test-token", server.URL, "")
	err := p.AddLabelsToPR(context.Background(), "owner/repo", 42, nil)
	require.NoError(t, err)
	assert.False(t, called, "no HTTP call for empty labels")
}

func TestGitLabProvider_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"403 Forbidden"}`))
	}))
	defer server.Close()

	p := scm.NewGitLabProvider("bad-token", server.URL, "")
	_, _, err := p.OpenPR(context.Background(), "owner/repo", "Test", "body", "head", "main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

// ─── NewProvider factory tests ────────────────────────────────────────────────

func TestNewProvider_GitHub(t *testing.T) {
	p, err := scm.NewProvider("github", "token", "https://api.github.com", "")
	require.NoError(t, err)
	// require.NotNil also verifies the interface is non-nil.
	require.NotNil(t, p)
}

func TestNewProvider_GitLab(t *testing.T) {
	p, err := scm.NewProvider("gitlab", "token", "https://gitlab.com", "")
	require.NoError(t, err)
	require.NotNil(t, p)
}

func TestNewProvider_Unknown(t *testing.T) {
	_, err := scm.NewProvider("bitbucket", "token", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bitbucket")
}

func TestNewProvider_EmptyType_DefaultsToGitHub(t *testing.T) {
	p, err := scm.NewProvider("", "token", "", "")
	require.NoError(t, err)
	require.NotNil(t, p)
}
