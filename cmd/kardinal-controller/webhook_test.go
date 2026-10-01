// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// mockSCMProvider is a test double that returns a fixed WebhookEvent.
type mockSCMProvider struct {
	event scm.WebhookEvent
	err   error
}

func (m *mockSCMProvider) OpenPR(_ context.Context, _, _, _, _, _ string) (string, int, error) {
	return "", 0, nil
}
func (m *mockSCMProvider) ClosePR(_ context.Context, _ string, _ int) error { return nil }
func (m *mockSCMProvider) CommentOnPR(_ context.Context, _ string, _ int, _ string) error {
	return nil
}
func (m *mockSCMProvider) GetPRStatus(_ context.Context, _ string, _ int) (bool, bool, error) {
	return m.event.Merged, true, nil
}
func (m *mockSCMProvider) GetPRReviewStatus(_ context.Context, _ string, _ int) (bool, int, error) {
	return false, 0, nil
}
func (m *mockSCMProvider) ParseWebhookEvent(payload []byte, _ string) (scm.WebhookEvent, error) {
	return m.event, m.err
}
func (m *mockSCMProvider) AddLabelsToPR(_ context.Context, _ string, _ int, _ []string) error {
	return nil
}

func webhookScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

// TestWebhook_MarksPRStatusMerged_OnMerge verifies that a merged PR webhook
// marks the matching PRStatus CRD as merged. The PromotionStep is NOT advanced
// directly — that's the PRStatusReconciler's job (WH-1 elimination).
func TestWebhook_MarksPRStatusMerged_OnMerge(t *testing.T) {
	// PRStatus for PR #42 — not yet merged.
	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-bundle-1-prod", Namespace: "default"},
		Spec: v1alpha1.PRStatusSpec{
			PRURL:    "https://github.com/owner/repo/pull/42",
			PRNumber: 42,
			Repo:     "owner/repo",
		},
		Status: v1alpha1.PRStatusStatus{
			Open:   true,
			Merged: false,
		},
	}

	s := webhookScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(prs).
		WithStatusSubresource(prs).
		Build()

	mockSCM := &mockSCMProvider{
		event: scm.WebhookEvent{
			EventType:    "pull_request",
			Action:       "closed",
			Merged:       true,
			PRNumber:     42,
			RepoFullName: "owner/repo",
		},
	}

	server := newWebhookServerWithConfig(mockSCM, c, zerolog.Nop(), true)
	handler := server.Handler()

	body := []byte(`{"action":"closed","pull_request":{"number":42,"merged":true},"repository":{"full_name":"owner/repo"}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook/scm", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)

	// PRStatus CRD should be marked merged.
	var updatedPRS v1alpha1.PRStatus
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "prstatus-bundle-1-prod", Namespace: "default"}, &updatedPRS))
	assert.True(t, updatedPRS.Status.Merged, "PRStatus.status.merged should be true after webhook")
	assert.False(t, updatedPRS.Status.Open, "PRStatus.status.open should be false after merge")
	assert.NotNil(t, updatedPRS.Status.LastCheckedAt, "PRStatus.status.lastCheckedAt should be set")
}

// TestWebhook_RecordsMergeCommit verifies that the webhook writes the merge
// commit together with status.merged, fills it in when polling recorded the
// merge first, and never replaces one already recorded (#1307).
func TestWebhook_RecordsMergeCommit(t *testing.T) {
	const sha = "e7ddb9e5a1b2c3d4e5f60718293a4b5c6d7e8f90"
	tests := []struct {
		name   string
		status v1alpha1.PRStatusStatus
		event  string
		want   string
	}{
		{name: "open PR", status: v1alpha1.PRStatusStatus{Open: true}, event: sha, want: sha},
		{name: "merged by polling without the commit", status: v1alpha1.PRStatusStatus{Merged: true},
			event: sha, want: sha},
		{name: "commit already recorded", status: v1alpha1.PRStatusStatus{Merged: true, MergeCommitSHA: "d7d4d8a"},
			event: sha, want: "d7d4d8a"},
		{name: "event without the commit", status: v1alpha1.PRStatusStatus{Open: true}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prs := &v1alpha1.PRStatus{
				ObjectMeta: metav1.ObjectMeta{Name: "prstatus-bundle-1-prod", Namespace: "default"},
				Spec:       v1alpha1.PRStatusSpec{PRNumber: 42, Repo: "owner/repo"},
				Status:     tc.status,
			}
			c := fake.NewClientBuilder().WithScheme(webhookScheme()).
				WithObjects(prs).WithStatusSubresource(prs).Build()
			mockSCM := &mockSCMProvider{event: scm.WebhookEvent{
				EventType: "pull_request", Action: "closed", Merged: true,
				PRNumber: 42, RepoFullName: "owner/repo", MergeCommitSHA: tc.event,
			}}
			w := httptest.NewRecorder()
			newWebhookServerWithConfig(mockSCM, c, zerolog.Nop(), true).Handler()(w,
				httptest.NewRequest(http.MethodPost, "/webhook/scm", bytes.NewReader([]byte(`{}`))))
			assert.Equal(t, http.StatusNoContent, w.Code)

			var got v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(prs), &got))
			assert.True(t, got.Status.Merged)
			assert.Equal(t, tc.want, got.Status.MergeCommitSHA)
		})
	}
}

// TestWebhook_RejectsInvalidSignature verifies that a webhook with an invalid
// HMAC signature returns 401.
func TestWebhook_RejectsInvalidSignature(t *testing.T) {
	s := webhookScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()

	mockSCM := &mockSCMProvider{
		err: assert.AnError, // simulate signature validation failure
	}

	server := newWebhookServerWithConfig(mockSCM, c, zerolog.Nop(), true)
	handler := server.Handler()

	body := []byte(`{"action":"closed","pull_request":{"number":1,"merged":true}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook/scm", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256=invalid")
	w := httptest.NewRecorder()

	handler(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestWebhook_IgnoresNonMergeEvents verifies that non-merge events are no-ops.
func TestWebhook_IgnoresNonMergeEvents(t *testing.T) {
	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-test", Namespace: "default"},
		Spec: v1alpha1.PRStatusSpec{
			PRURL:    "https://github.com/owner/repo/pull/42",
			PRNumber: 42,
			Repo:     "owner/repo",
		},
		Status: v1alpha1.PRStatusStatus{Open: true},
	}

	s := webhookScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(prs).WithStatusSubresource(prs).Build()

	mockSCM := &mockSCMProvider{
		event: scm.WebhookEvent{
			EventType: "pull_request",
			Action:    "opened", // NOT closed
			Merged:    false,
			PRNumber:  42,
		},
	}

	server := newWebhookServerWithConfig(mockSCM, c, zerolog.Nop(), true)
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/webhook/scm", bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()
	handler(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)

	// PRStatus should be unchanged.
	var updated v1alpha1.PRStatus
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "prstatus-test", Namespace: "default"}, &updated))
	assert.False(t, updated.Status.Merged, "PRStatus.status.merged should remain false for non-merge events")
}

// TestWebhookHealth_ReturnsOK verifies that GET /webhook/scm/health returns 200
// with a JSON body containing status, webhookConfigured, and eventsProcessed fields.
func TestWebhookHealth_ReturnsOK(t *testing.T) {
	s := webhookScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()

	server := newWebhookServerWithConfig(&mockSCMProvider{}, c, zerolog.Nop(), true)
	handler := server.HealthHandler()

	req := httptest.NewRequest(http.MethodGet, "/webhook/scm/health", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	body := w.Body.String()
	assert.Contains(t, body, `"status"`)
	assert.Contains(t, body, `"webhookConfigured"`)
	assert.Contains(t, body, `"eventsProcessed"`)
}

// TestWebhookHealth_ReflectsWebhookUnconfigured verifies that the health endpoint
// returns webhookConfigured=false when no secret is set.
func TestWebhookHealth_ReflectsWebhookUnconfigured(t *testing.T) {
	s := webhookScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()

	server := newWebhookServerWithConfig(&mockSCMProvider{}, c, zerolog.Nop(), false)
	handler := server.HealthHandler()

	req := httptest.NewRequest(http.MethodGet, "/webhook/scm/health", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"webhookConfigured":false`)
}

func webhookPRS(name, ns, repo string, pr int) *v1alpha1.PRStatus {
	return &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       v1alpha1.PRStatusSpec{PRNumber: pr, Repo: repo},
		Status:     v1alpha1.PRStatusStatus{Open: pr > 0},
	}
}

func webhookMerged(t *testing.T, c client.Client, name, ns string) bool {
	t.Helper()
	var p v1alpha1.PRStatus
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: ns}, &p))
	return p.Status.Merged
}

// TestWebhook_FailsClosed verifies that the SCM webhook uses the real GitHub
// provider to reject forged merge events: with no secret configured every
// request is rejected, and with a secret only a valid HMAC is accepted.
// Regression test for C07-controller-01, C06-scm-health-01 and E2E-17.
func TestWebhook_FailsClosed(t *testing.T) {
	const secret = "test-secret"
	forged := `{"action":"closed","pull_request":{"number":7,"merged":true},"repository":{"full_name":"org/app"}}`
	noNumber := `{"action":"closed","pull_request":{"merged":true}}`
	oversized := forged + strings.Repeat(" ", maxWebhookBody)
	sign := func(key, body string) string {
		m := hmac.New(sha256.New, []byte(key))
		m.Write([]byte(body))
		return "sha256=" + hex.EncodeToString(m.Sum(nil))
	}
	tests := []struct {
		name       string
		secret     string
		body       string
		signature  string
		wantCode   int
		wantMerged bool
	}{
		{name: "no secret, unsigned", secret: "", body: forged, wantCode: http.StatusUnauthorized},
		{name: "no secret, unsigned, no PR number", secret: "", body: noNumber, wantCode: http.StatusUnauthorized},
		{name: "no secret, empty-key HMAC", secret: "", body: forged, signature: sign("", forged), wantCode: http.StatusUnauthorized},
		{name: "secret, unsigned", secret: secret, body: forged, wantCode: http.StatusUnauthorized},
		{name: "secret, wrong key", secret: secret, body: forged, signature: sign("other", forged), wantCode: http.StatusUnauthorized},
		{name: "secret, valid signature", secret: secret, body: forged, signature: sign(secret, forged), wantCode: http.StatusNoContent, wantMerged: true},
		// C07-controller-25: an oversized body is rejected with 413 instead of
		// being truncated and misreported as a bad signature (401).
		{name: "secret, valid signature, oversized body", secret: secret, body: oversized, signature: sign(secret, oversized),
			wantCode: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := scm.NewProvider("github", "", "", tt.secret)
			require.NoError(t, err)
			c := fake.NewClientBuilder().WithScheme(webhookScheme()).
				WithObjects(webhookPRS("prs", "default", "org/app", 7), webhookPRS("placeholder", "default", "", 0)).
				WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
			srv := newWebhookServerWithConfig(p, c, zerolog.Nop(), tt.secret != "")

			req := httptest.NewRequest(http.MethodPost, "/webhook/scm", strings.NewReader(tt.body))
			if tt.signature != "" {
				req.Header.Set("X-Hub-Signature-256", tt.signature)
			}
			w := httptest.NewRecorder()
			srv.Handler()(w, req)

			assert.Equal(t, tt.wantCode, w.Code)
			assert.Equal(t, tt.wantMerged, webhookMerged(t, c, "prs", "default"))
			assert.False(t, webhookMerged(t, c, "placeholder", "default"), "placeholder PRStatus must never be marked merged")
		})
	}
}

// TestWebhook_MergeEventScoping verifies that an authenticated merge event only
// marks the PRStatus whose PR number and repo both match. Events without a PR
// number or repo, and placeholder PRStatus objects (PR not open yet), are never
// matched. Regression test for C07-controller-01 and E2E-17.
func TestWebhook_MergeEventScoping(t *testing.T) {
	tests := []struct {
		name       string
		event      scm.WebhookEvent
		wantMerged map[string]bool
	}{
		{
			name:       "matching number and repo",
			event:      scm.WebhookEvent{PRNumber: 7, RepoFullName: "org/app"},
			wantMerged: map[string]bool{"app-7": true, "svc-7": false, "placeholder": false, "norepo-7": false},
		},
		{
			name:       "repo compared case-insensitively",
			event:      scm.WebhookEvent{PRNumber: 7, RepoFullName: "Org/App"},
			wantMerged: map[string]bool{"app-7": true, "svc-7": false, "placeholder": false, "norepo-7": false},
		},
		{
			name:       "event without repo matches nothing",
			event:      scm.WebhookEvent{PRNumber: 7},
			wantMerged: map[string]bool{"app-7": false, "svc-7": false, "placeholder": false, "norepo-7": false},
		},
		{
			name:       "event without PR number matches nothing",
			event:      scm.WebhookEvent{RepoFullName: "org/app"},
			wantMerged: map[string]bool{"app-7": false, "svc-7": false, "placeholder": false, "norepo-7": false},
		},
		{
			name:       "event without PR number or repo matches nothing",
			event:      scm.WebhookEvent{},
			wantMerged: map[string]bool{"app-7": false, "svc-7": false, "placeholder": false, "norepo-7": false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(webhookScheme()).
				WithObjects(
					webhookPRS("app-7", "team-a", "org/app", 7),
					webhookPRS("svc-7", "team-b", "other/svc", 7),
					webhookPRS("placeholder", "team-c", "", 0),
					webhookPRS("norepo-7", "team-d", "", 7),
				).
				WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
			ev := tt.event
			ev.EventType, ev.Action, ev.Merged = "pull_request", "closed", true
			srv := newWebhookServerWithConfig(&mockSCMProvider{event: ev}, c, zerolog.Nop(), true)

			w := httptest.NewRecorder()
			srv.Handler()(w, httptest.NewRequest(http.MethodPost, "/webhook/scm", strings.NewReader(`{}`)))
			require.Equal(t, http.StatusNoContent, w.Code)

			ns := map[string]string{"app-7": "team-a", "svc-7": "team-b", "placeholder": "team-c", "norepo-7": "team-d"}
			for name, want := range tt.wantMerged {
				assert.Equal(t, want, webhookMerged(t, c, name, ns[name]), name)
			}
		})
	}
}

// TestWebhook_EventTypeFromHeader sends signed deliveries the way GitHub,
// Forgejo, Gitea and GitLab send them (event header plus payload) through the
// handler with the real providers. Push and comment events must be logged
// with their own type, and only a merged pull request may mark the PRStatus
// merged or count in mergedPREvents on /webhook/scm/health.
func TestWebhook_EventTypeFromHeader(t *testing.T) {
	const secret = "s3cret"
	hexMAC := func(body string) string {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte(body))
		return hex.EncodeToString(m.Sum(nil))
	}
	// Gitea sends X-Gitea-Event with the coarse event name, X-Gitea-Event-Type
	// with the fine one, and X-GitHub-Event for compatibility. Forgejo also
	// sends X-Forgejo-Event and X-Forgejo-Event-Type.
	gitea := func(event, eventType string) func(string) http.Header {
		return func(body string) http.Header {
			h := http.Header{}
			h.Set("X-Gitea-Event", event)
			h.Set("X-Gitea-Event-Type", eventType)
			h.Set("X-GitHub-Event", event)
			h.Set("X-Gitea-Signature", hexMAC(body))
			return h
		}
	}
	forgejo := func(event, eventType string) func(string) http.Header {
		return func(body string) http.Header {
			h := gitea(event, eventType)(body)
			h.Set("X-Forgejo-Event", event)
			h.Set("X-Forgejo-Event-Type", eventType)
			h.Set("X-Forgejo-Signature", hexMAC(body))
			return h
		}
	}
	github := func(event string) func(string) http.Header {
		return func(body string) http.Header {
			h := http.Header{}
			h.Set("X-GitHub-Event", event)
			h.Set("X-Hub-Signature-256", "sha256="+hexMAC(body))
			return h
		}
	}
	gitlab := func(event string) func(string) http.Header {
		return func(string) http.Header {
			h := http.Header{}
			h.Set("X-Gitlab-Event", event)
			h.Set("X-Gitlab-Token", secret)
			return h
		}
	}
	const (
		giteaPush  = `{"ref":"refs/heads/main","before":"a1","after":"b2","repository":{"full_name":"o/r"}}`
		giteaMerge = `{"action":"closed","number":5,"pull_request":{"number":5,"merged":true},"repository":{"full_name":"o/r"}}`
	)
	tests := []struct {
		name       string
		provider   string
		header     func(body string) http.Header
		body       string
		wantType   string
		wantAction string
		// wantMerged: the PRStatus is marked merged and the event counts in
		// mergedPREvents.
		wantMerged bool
	}{
		{name: "forgejo push", provider: "forgejo", header: forgejo("push", "push"), body: giteaPush,
			wantType: "push"},
		{name: "gitea push", provider: "gitea", header: gitea("push", "push"), body: giteaPush,
			wantType: "push"},
		{name: "gitea comment on a merged PR", provider: "gitea",
			header:   gitea("issue_comment", "pull_request_comment"),
			body:     `{"action":"created","issue":{"number":5},"pull_request":{"number":5,"merged":true},"is_pull":true,"comment":{"body":"verified"},"repository":{"full_name":"o/r"}}`,
			wantType: "issue_comment", wantAction: "created"},
		{name: "forgejo label change on a merged PR", provider: "forgejo",
			header:   forgejo("pull_request", "pull_request_label"),
			body:     `{"action":"label_updated","number":5,"pull_request":{"number":5,"merged":true},"repository":{"full_name":"o/r"}}`,
			wantType: "pull_request", wantAction: "label_updated"},
		{name: "forgejo merge", provider: "forgejo", header: forgejo("pull_request", "pull_request"), body: giteaMerge,
			wantType: "pull_request", wantAction: "closed", wantMerged: true},
		{name: "gitea merge", provider: "gitea", header: gitea("pull_request", "pull_request"), body: giteaMerge,
			wantType: "pull_request", wantAction: "closed", wantMerged: true},
		{name: "github ping", provider: "github", header: github("ping"),
			body:     `{"zen":"Keep it logically awesome.","hook_id":1,"repository":{"full_name":"o/r"}}`,
			wantType: "ping"},
		{name: "github push", provider: "github", header: github("push"),
			body:     `{"ref":"refs/heads/main","repository":{"full_name":"o/r"}}`,
			wantType: "push"},
		{name: "github comment on a PR", provider: "github", header: github("issue_comment"),
			body:     `{"action":"created","issue":{"number":5,"pull_request":{}},"comment":{"body":"verified"},"repository":{"full_name":"o/r"}}`,
			wantType: "issue_comment", wantAction: "created"},
		{name: "github review of a merged PR", provider: "github", header: github("pull_request_review"),
			body:     `{"action":"submitted","pull_request":{"number":5,"merged":true},"repository":{"full_name":"o/r"}}`,
			wantType: "pull_request_review", wantAction: "submitted"},
		{name: "github merge", provider: "github", header: github("pull_request"),
			body:     `{"action":"closed","pull_request":{"number":5,"merged":true},"repository":{"full_name":"o/r"}}`,
			wantType: "pull_request", wantAction: "closed", wantMerged: true},
		{name: "gitlab push", provider: "gitlab", header: gitlab("Push Hook"),
			body:     `{"object_kind":"push","ref":"refs/heads/main","project":{"path_with_namespace":"o/r"}}`,
			wantType: "push"},
		{name: "gitlab comment on a merged MR", provider: "gitlab", header: gitlab("Note Hook"),
			body:     `{"object_kind":"note","object_attributes":{"note":"verified"},"merge_request":{"iid":5,"state":"merged"},"project":{"path_with_namespace":"o/r"}}`,
			wantType: "note"},
		{name: "gitlab MR opened", provider: "gitlab", header: gitlab("Merge Request Hook"),
			body:     `{"object_kind":"merge_request","object_attributes":{"iid":5,"state":"opened","action":"open"},"project":{"path_with_namespace":"o/r"}}`,
			wantType: "merge_request", wantAction: "open"},
		{name: "gitlab merge", provider: "gitlab", header: gitlab("Merge Request Hook"),
			body:     `{"object_kind":"merge_request","object_attributes":{"iid":5,"state":"merged","action":"merge"},"project":{"path_with_namespace":"o/r"}}`,
			wantType: "pull_request", wantAction: "closed", wantMerged: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := scm.NewProvider(tc.provider, "", "", secret)
			require.NoError(t, err)
			c := fake.NewClientBuilder().WithScheme(webhookScheme()).
				WithObjects(webhookPRS("prs", "default", "o/r", 5)).
				WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
			var logs bytes.Buffer
			srv := newWebhookServerWithConfig(p, c, zerolog.New(&logs), true)

			req := httptest.NewRequest(http.MethodPost, "/webhook/scm", strings.NewReader(tc.body))
			req.Header = tc.header(tc.body)
			w := httptest.NewRecorder()
			srv.Handler()(w, req)
			require.Equal(t, http.StatusNoContent, w.Code, logs.String())

			var received map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				if strings.Contains(line, `"message":"webhook received"`) {
					require.NoError(t, json.Unmarshal([]byte(line), &received))
				}
			}
			require.NotNil(t, received, "no \"webhook received\" line in:\n%s", logs.String())
			assert.Equal(t, tc.wantType, received["event_type"], "logged event_type")
			assert.Equal(t, tc.wantAction, received["action"], "logged action")

			assert.Equal(t, tc.wantMerged, webhookMerged(t, c, "prs", "default"), "PRStatus merged")

			hw := httptest.NewRecorder()
			srv.HealthHandler()(hw, httptest.NewRequest(http.MethodGet, "/webhook/scm/health", nil))
			var health map[string]any
			require.NoError(t, json.Unmarshal(hw.Body.Bytes(), &health))
			assert.Equal(t, float64(1), health["eventsProcessed"], "every signed event is counted: %s", hw.Body.String())
			wantMerges := float64(0)
			if tc.wantMerged {
				wantMerges = 1
			}
			assert.Equal(t, wantMerges, health["mergedPREvents"], "only a merge counts as a merge: %s", hw.Body.String())
		})
	}
}
