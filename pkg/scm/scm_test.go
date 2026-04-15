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
		UpstreamEnvironments: []scm.PRBodyUpstreamEnv{
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
		UpstreamEnvironments: []scm.PRBodyUpstreamEnv{
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
	// go-git hangs on macOS when PlainInit or PlainOpen are called on certain filesystem paths.
	// Skip on non-Linux platforms; the real behavior is validated in CI (Linux) and PDCA workflow.
	if testing.Short() {
		t.Skip("skipping go-git test in short mode")
	}
	// Push against a non-git directory should fail at PlainOpen (before any network call).
	c := scm.NewGoGitClient()
	err := c.Push(context.Background(), t.TempDir(), "origin", "main", "tok")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open repo")
}

// TestGoGitClient_CheckoutCommitErrors verifies error propagation for repository
// operations against non-git directories.
// Note: go-git PlainInit/PlainOpen can hang on macOS in certain filesystem configurations.
// These tests are skipped in short mode and run in CI (Linux only).
func TestGoGitClient_CheckoutCommitErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-git filesystem tests in short mode (may hang on macOS)")
	}
	ctx := context.Background()
	c := scm.NewGoGitClient()

	// Checkout: non-git directory → error, not panic.
	err := c.Checkout(ctx, t.TempDir(), "feat/test")
	require.Error(t, err, "Checkout of non-repo must fail")
	assert.NotPanics(t, func() { _ = err.Error() })

	// CommitAll: non-git directory → error.
	err = c.CommitAll(ctx, t.TempDir(), "msg", "Author", "a@b.com")
	require.Error(t, err, "CommitAll on non-repo must fail")
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

// TestRenderPRBody_ElapsedPrecomputed verifies that upstream environment elapsed
// time is pre-computed by the caller and rendered verbatim from PRBodyUpstreamEnv.Elapsed.
// This eliminates SCM-4: time.Since() called inside PR template execution.
func TestRenderPRBody_ElapsedPrecomputed(t *testing.T) {
	healthCheckedAt := metav1.NewTime(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))
	data := scm.PRBody{
		PipelineName: "nginx-demo",
		Environment:  "prod",
		BundleName:   "nginx-demo-v1-29-0",
		Bundle:       v1alpha1.BundleSpec{Type: "image"},
		UpstreamEnvironments: []scm.PRBodyUpstreamEnv{
			{Name: "uat", Phase: "Verified", HealthCheckedAt: &healthCheckedAt, Elapsed: "45m"},
			{Name: "test", Phase: "Verified", HealthCheckedAt: nil, Elapsed: ""},
		},
	}

	body, err := scm.RenderPRBody(data)
	require.NoError(t, err)

	// Pre-computed elapsed must appear verbatim — no time.Since in template
	assert.Contains(t, body, "45m", "pre-computed elapsed must appear in PR body")
	assert.Contains(t, body, "uat")
	assert.Contains(t, body, "test")
	assert.Contains(t, body, "Upstream Verification")
}

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

// ─── Forgejo/Gitea SCM Provider Tests ─────────────────────────────────────────

func TestForgejoProvider_OpenPR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/repos/owner/repo/pulls", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.Header.Get("Authorization"), "token test-token")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"number":   55,
			"html_url": "https://forgejo.example.com/owner/repo/pulls/55",
		})
	}))
	defer server.Close()

	p := scm.NewForgejoProvider("test-token", server.URL, "")
	url, num, err := p.OpenPR(context.Background(), "owner/repo", "Test PR", "body", "feature", "main")
	require.NoError(t, err)
	assert.Equal(t, 55, num)
	assert.Equal(t, "https://forgejo.example.com/owner/repo/pulls/55", url)
}

func TestForgejoProvider_OpenPR_AlreadyExists(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			// First call: POST to create → 409 conflict
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"pull request already exists"}`))
			return
		}
		// Second call: GET to list open PRs
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"number":   55,
				"html_url": "https://forgejo.example.com/owner/repo/pulls/55",
				"head":     map[string]interface{}{"label": "owner:feature"},
			},
		})
	}))
	defer server.Close()

	p := scm.NewForgejoProvider("test-token", server.URL, "")
	url, num, err := p.OpenPR(context.Background(), "owner/repo", "Test PR", "body", "feature", "main")
	require.NoError(t, err)
	assert.Equal(t, 55, num)
	assert.Equal(t, "https://forgejo.example.com/owner/repo/pulls/55", url)
}

func TestForgejoProvider_ClosePR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/repos/owner/repo/pulls/55", r.URL.Path)
		assert.Equal(t, http.MethodPatch, r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := scm.NewForgejoProvider("test-token", server.URL, "")
	require.NoError(t, p.ClosePR(context.Background(), "owner/repo", 55))
}

func TestForgejoProvider_CommentOnPR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/repos/owner/repo/issues/55/comments", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	p := scm.NewForgejoProvider("test-token", server.URL, "")
	require.NoError(t, p.CommentOnPR(context.Background(), "owner/repo", 55, "hello"))
}

func TestForgejoProvider_GetPRStatus(t *testing.T) {
	tests := []struct {
		name       string
		apiState   string
		apiMerged  bool
		wantMerged bool
		wantOpen   bool
	}{
		{name: "open", apiState: "open", apiMerged: false, wantMerged: false, wantOpen: true},
		{name: "merged", apiState: "closed", apiMerged: true, wantMerged: true, wantOpen: false},
		{name: "closed_unmerged", apiState: "closed", apiMerged: false, wantMerged: false, wantOpen: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"state":  tc.apiState,
					"merged": tc.apiMerged,
				})
			}))
			defer server.Close()

			p := scm.NewForgejoProvider("test-token", server.URL, "")
			merged, open, err := p.GetPRStatus(context.Background(), "owner/repo", 55)
			require.NoError(t, err)
			assert.Equal(t, tc.wantMerged, merged, "merged mismatch")
			assert.Equal(t, tc.wantOpen, open, "open mismatch")
		})
	}
}

func TestForgejoProvider_ParseWebhookEvent_ValidSignature(t *testing.T) {
	secret := "forgejo-secret"
	payload := []byte(`{"action":"closed","number":55,"pull_request":{"merged":true,"state":"closed","html_url":"https://forgejo.example.com/owner/repo/pulls/55"},"repository":{"full_name":"owner/repo"}}`)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	sig := hex.EncodeToString(mac.Sum(nil))

	p := scm.NewForgejoProvider("token", "", secret)
	evt, err := p.ParseWebhookEvent(payload, sig)
	require.NoError(t, err)
	assert.Equal(t, 55, evt.PRNumber)
	assert.Equal(t, "owner/repo", evt.RepoFullName)
	assert.True(t, evt.Merged)
}

func TestForgejoProvider_ParseWebhookEvent_InvalidSignature(t *testing.T) {
	p := scm.NewForgejoProvider("token", "", "correct-secret")
	_, err := p.ParseWebhookEvent([]byte(`{}`), "wrong-sig")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HMAC")
}

func TestForgejoProvider_ParseWebhookEvent_NoSecret(t *testing.T) {
	p := scm.NewForgejoProvider("token", "", "")
	evt, err := p.ParseWebhookEvent([]byte(`{"action":"opened","number":10,"pull_request":{},"repository":{"full_name":"owner/repo"}}`), "")
	require.NoError(t, err)
	assert.Equal(t, 10, evt.PRNumber)
}

func TestForgejoProvider_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
	}))
	defer server.Close()

	p := scm.NewForgejoProvider("bad-token", server.URL, "")
	_, _, err := p.OpenPR(context.Background(), "owner/repo", "Test", "body", "head", "main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

// ─── NewProvider factory tests (Forgejo) ─────────────────────────────────────

func TestNewProvider_Forgejo(t *testing.T) {
	p, err := scm.NewProvider("forgejo", "token", "https://codeberg.org", "")
	require.NoError(t, err)
	require.NotNil(t, p)
}

func TestNewProvider_Gitea(t *testing.T) {
	p, err := scm.NewProvider("gitea", "token", "https://gitea.example.com", "")
	require.NoError(t, err)
	require.NotNil(t, p)
}

// TestRenderPRBody_RollbackSection verifies that rollback PRs include a rollback
// notice section with the bundle being rolled back to and the one being reverted.
// This is required by issue #402 — rollback PR body must contain structured evidence.
func TestRenderPRBody_RollbackSection(t *testing.T) {
	data := scm.PRBody{
		PipelineName: "nginx-demo",
		Environment:  "prod",
		BundleName:   "nginx-demo-rollback-abc123",
		RollbackOf:   "nginx-demo-bad-version",
		Bundle: v1alpha1.BundleSpec{
			Type: "image",
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/nginx/nginx", Tag: "1.29.0"},
			},
			Provenance: &v1alpha1.BundleProvenance{
				CommitSHA:  "abc123",
				Author:     "ci",
				RollbackOf: "nginx-demo-bad-version",
			},
		},
	}

	body, err := scm.RenderPRBody(data)
	require.NoError(t, err)

	// Rollback PR must contain the rollback notice (#402)
	assert.Contains(t, body, "ROLLBACK", "rollback PR body must contain ROLLBACK header")
	assert.Contains(t, body, "nginx-demo-bad-version",
		"rollback PR body must mention the bundle being reverted")
	assert.Contains(t, body, "nginx-demo-rollback-abc123",
		"rollback PR body must mention the new rollback bundle name")
	assert.Contains(t, body, "prod", "rollback PR body must mention the environment")
	// Rollback body must NOT show standard 'Promotion:' header
	assert.NotContains(t, body, "## Promotion:",
		"rollback PR must use ROLLBACK header, not Promotion header")
	t.Logf("rollback PR body:\n%s", body)
}

// TestRenderPRBody_NormalPromotion_NoRollbackSection verifies that normal promotion
// PRs do NOT include the rollback section.
func TestRenderPRBody_NormalPromotion_NoRollbackSection(t *testing.T) {
	data := scm.PRBody{
		PipelineName: "nginx-demo",
		Environment:  "prod",
		BundleName:   "nginx-demo-v1-29-0",
		// RollbackOf intentionally empty
		Bundle: v1alpha1.BundleSpec{
			Type: "image",
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/nginx/nginx", Tag: "1.29.0"},
			},
		},
	}

	body, err := scm.RenderPRBody(data)
	require.NoError(t, err)

	assert.NotContains(t, body, "ROLLBACK",
		"normal promotion PR must NOT have ROLLBACK header")
	assert.Contains(t, body, "Promotion:",
		"normal promotion PR must have standard Promotion header")
}

// TestPRBodyDocumentedFields verifies that the PR body contains every field
// documented in docs/pr-evidence.md. This is the automated evidence for issue #412
// (proof(PR evidence): every prod PR on kardinal-demo contains all required fields).
//
// The test exercises the full template with realistic data and asserts each documented
// field is present. A failure here means the PR body that users see is missing evidence.
func TestPRBodyDocumentedFields(t *testing.T) {
	evalTime := metav1.NewTime(time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC))
	healthCheckedAt := metav1.NewTime(time.Date(2026, 4, 13, 13, 15, 0, 0, time.UTC))

	data := scm.PRBody{
		PipelineName: "my-app",
		Environment:  "prod",
		BundleName:   "my-app-v1-29-0",
		RepoURL:      "https://github.com/pnz1990/kardinal-demo",
		Bundle: v1alpha1.BundleSpec{
			Type: "image",
			Images: []v1alpha1.ImageRef{
				{
					Repository: "ghcr.io/pnz1990/kardinal-test-app",
					Tag:        "sha-abc1234",
					Digest:     "sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
				},
			},
			Provenance: &v1alpha1.BundleProvenance{
				CommitSHA: "abc1234def5678",
				CIRunURL:  "https://github.com/pnz1990/kardinal-test-app/actions/runs/12345",
				Author:    "platform-engineer",
			},
		},
		GateResults: []v1alpha1.GateResult{
			{
				GateName:      "no-weekend-deploys",
				GateNamespace: "platform-policies",
				Result:        "pass",
				Reason:        "Monday 14:00 UTC — not a weekend",
				EvaluatedAt:   evalTime,
			},
			{
				GateName:      "staging-soak-30m",
				GateNamespace: "platform-policies",
				Result:        "pass",
				Reason:        "soakMinutes=45 >= 30",
				EvaluatedAt:   evalTime,
			},
		},
		UpstreamEnvironments: []scm.PRBodyUpstreamEnv{
			{
				Name:            "test",
				Phase:           "Verified",
				HealthCheckedAt: &healthCheckedAt,
				Elapsed:         "2h45m",
			},
			{
				Name:            "uat",
				Phase:           "Verified",
				HealthCheckedAt: &healthCheckedAt,
				Elapsed:         "45m",
			},
		},
		PreviousCommitSHA: "prevcommit1234",
	}

	body, err := scm.RenderPRBody(data)
	require.NoError(t, err)
	t.Logf("Full PR body:\n%s", body)

	tests := []struct {
		field    string // human-readable field name from docs/pr-evidence.md
		contains string // substring that must appear in the PR body
	}{
		// Header
		{"bundle name", "my-app-v1-29-0"},
		{"pipeline name", "my-app"},
		{"target environment", "prod"},
		// Artifact Provenance table
		{"provenance table header", "Artifact Provenance"},
		{"image repository", "ghcr.io/pnz1990/kardinal-test-app"},
		{"image tag", "sha-abc1234"},
		{"image digest (sha256)", "sha256:a1b2c3d4"},
		{"ci run url link", "github.com/pnz1990/kardinal-test-app/actions/runs/12345"},
		{"source commit sha", "abc1234def5678"},
		{"author", "platform-engineer"},
		// Policy Gate Compliance table
		{"gate compliance table header", "Policy Gate Compliance"},
		{"gate name", "no-weekend-deploys"},
		{"gate namespace", "platform-policies"},
		{"gate result", "pass"},
		{"gate reason", "not a weekend"},
		{"soak gate name", "staging-soak-30m"},
		{"soak gate reason", "soakMinutes=45"},
		// Upstream Verification table
		{"upstream verification table header", "Upstream Verification"},
		{"upstream env test", "test"},
		{"upstream env uat", "uat"},
		{"upstream elapsed uat", "45m"},
		{"upstream elapsed test", "2h45m"},
		// Source diff link (PreviousCommitSHA provided)
		{"source diff section", "Source Diff"},
		{"diff link contains prev sha", "prevcommit1234"},
		{"diff link contains new sha", "abc1234def5678"},
		// Template identifier
		{"kardinal-promoter footer", "kardinal-promoter"},
	}

	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			assert.True(t,
				strings.Contains(body, tc.contains),
				"PR body must contain %q for field %q\nActual body:\n%s",
				tc.contains, tc.field, body,
			)
		})
	}
}

// TestFormatElapsed verifies the pre-computed elapsed time formatting used to
// eliminate time.Since() calls inside the PR template (SCM-4 logic leak fix).
func TestFormatElapsed(t *testing.T) {
	now := time.Date(2026, 4, 13, 14, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		since time.Time
		want  string
	}{
		{"30 seconds ago", now.Add(-30 * time.Second), "0m"},
		{"5 minutes ago", now.Add(-5 * time.Minute), "5m"},
		{"45 minutes ago", now.Add(-45 * time.Minute), "45m"},
		{"2 hours 30 minutes ago", now.Add(-150 * time.Minute), "2h30m"},
		{"exactly 1 hour", now.Add(-60 * time.Minute), "1h0m"},
		{"3 days ago", now.Add(-72 * time.Hour), "3d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scm.FormatElapsed(tc.since, now)
			assert.Equal(t, tc.want, got)
		})
	}
}
