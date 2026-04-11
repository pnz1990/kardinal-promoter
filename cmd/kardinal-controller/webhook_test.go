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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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

// TestWebhook_AdvancesPromotionStep_OnMerge verifies that a merged PR webhook
// transitions a WaitingForMerge PromotionStep to HealthChecking.
func TestWebhook_AdvancesPromotionStep_OnMerge(t *testing.T) {
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-wfm", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-1",
			Environment:  "prod",
			StepType:     "pr-review",
		},
		Status: v1alpha1.PromotionStepStatus{
			State: "WaitingForMerge",
			PRURL: "https://github.com/owner/repo/pull/42",
			Outputs: map[string]string{
				"prNumber": "42",
				"prURL":    "https://github.com/owner/repo/pull/42",
			},
		},
	}

	s := webhookScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(ps).
		WithStatusSubresource(ps).
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

	server := newWebhookServer(mockSCM, c, zerolog.Nop())
	handler := server.Handler()

	body := []byte(`{"action":"closed","pull_request":{"number":42,"merged":true},"repository":{"full_name":"owner/repo"}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook/scm", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-wfm", Namespace: "default"}, &updated))
	assert.Equal(t, "HealthChecking", updated.Status.State)
}

// TestWebhook_RejectsInvalidSignature verifies that a webhook with an invalid
// HMAC signature returns 401.
func TestWebhook_RejectsInvalidSignature(t *testing.T) {
	s := webhookScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()

	mockSCM := &mockSCMProvider{
		err: assert.AnError, // simulate signature validation failure
	}

	server := newWebhookServer(mockSCM, c, zerolog.Nop())
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
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-wfm", Namespace: "default"},
		Status: v1alpha1.PromotionStepStatus{
			State:   "WaitingForMerge",
			Outputs: map[string]string{"prNumber": "42"},
			PRURL:   "https://github.com/owner/repo/pull/42",
		},
	}

	s := webhookScheme()
	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(ps).WithStatusSubresource(ps).Build()

	mockSCM := &mockSCMProvider{
		event: scm.WebhookEvent{
			EventType: "pull_request",
			Action:    "opened", // NOT closed
			Merged:    false,
			PRNumber:  42,
		},
	}

	server := newWebhookServer(mockSCM, c, zerolog.Nop())
	handler := server.Handler()

	req := httptest.NewRequest(http.MethodPost, "/webhook/scm", bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()
	handler(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)

	// Step state should be unchanged.
	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-wfm", Namespace: "default"}, &updated))
	assert.Equal(t, "WaitingForMerge", updated.Status.State)
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
