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
