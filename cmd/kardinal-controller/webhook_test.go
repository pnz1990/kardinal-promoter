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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone/objectgonetest"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// mockSCMProvider is a test double that returns a fixed WebhookEvent. Its
// GetPRStatus reports the PR merged when the event says so, unless notMerged
// or statusErr is set, and counts the calls. With hang set it stands in for
// an SCM that answers only when the caller gives up: it records the deadline
// of the context it gets (statusDeadline, statusHasDeadline) and ends the
// call with context.DeadlineExceeded at once, so a test does not wait for the
// deadline; without one it waits for the context, as the real call would.
type mockSCMProvider struct {
	event             scm.WebhookEvent
	err               error
	notMerged         bool
	statusErr         error
	statusCalls       int
	hang              bool
	statusDeadline    time.Time
	statusHasDeadline bool
}

func (m *mockSCMProvider) OpenPR(_ context.Context, _, _, _, _, _ string) (string, int, error) {
	return "", 0, nil
}
func (m *mockSCMProvider) ClosePR(_ context.Context, _ string, _ int) error { return nil }
func (m *mockSCMProvider) CommentOnPR(_ context.Context, _ string, _ int, _ string) error {
	return nil
}
func (m *mockSCMProvider) GetPRStatus(ctx context.Context, _ string, _ int) (bool, bool, error) {
	m.statusCalls++
	if m.hang {
		m.statusDeadline, m.statusHasDeadline = ctx.Deadline()
		if !m.statusHasDeadline {
			<-ctx.Done()
		}
		return false, false, context.DeadlineExceeded
	}
	if m.statusErr != nil {
		return false, false, m.statusErr
	}
	return m.event.Merged && !m.notMerged, true, nil
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
// merge first, and never replaces one already recorded (#1307). A commit it
// writes clears status.mergeCommitUnavailable: the status holds one of the two.
func TestWebhook_RecordsMergeCommit(t *testing.T) {
	const sha = "e7ddb9e5a1b2c3d4e5f60718293a4b5c6d7e8f90"
	tests := []struct {
		name            string
		status          v1alpha1.PRStatusStatus
		event           string
		want            string
		wantUnavailable bool
	}{
		{name: "open PR", status: v1alpha1.PRStatusStatus{Open: true}, event: sha, want: sha},
		{name: "merged by polling without the commit", status: v1alpha1.PRStatusStatus{Merged: true},
			event: sha, want: sha},
		{name: "commit already recorded", status: v1alpha1.PRStatusStatus{Merged: true, MergeCommitSHA: "d7d4d8a"},
			event: sha, want: "d7d4d8a"},
		{name: "event without the commit", status: v1alpha1.PRStatusStatus{Open: true}, want: ""},
		{name: "commit recorded unavailable, the event has it", status: v1alpha1.PRStatusStatus{
			Merged: true, MergeCommitUnavailable: true}, event: sha, want: sha},
		{name: "commit recorded unavailable, the event has none", status: v1alpha1.PRStatusStatus{
			Merged: true, MergeCommitUnavailable: true}, want: "", wantUnavailable: true},
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
			assert.Equal(t, tc.wantUnavailable, got.Status.MergeCommitUnavailable)
		})
	}
}

// TestWebhook_StatusOfTheOldPR covers B72: a PRStatus whose spec was pointed
// at a new PR (a recreated step) still has the old PR's status until the
// PRStatus reconciler clears it. A merge of the new PR replaces that status
// and records the generation, so the clear does not undo the merge.
func TestWebhook_StatusOfTheOldPR(t *testing.T) {
	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-bundle-1-prod", Namespace: "default", Generation: 2},
		Spec:       v1alpha1.PRStatusSpec{PRURL: "https://github.com/owner/repo/pull/43", PRNumber: 43, Repo: "owner/repo"},
		Status: v1alpha1.PRStatusStatus{ClosedFinal: true, PollError: "status 404", Approved: true,
			ApprovalCount: 2, ObservedGeneration: 1},
	}
	c := fake.NewClientBuilder().WithScheme(webhookScheme()).
		WithObjects(prs).WithStatusSubresource(prs).Build()
	mockSCM := &mockSCMProvider{event: scm.WebhookEvent{
		EventType: "pull_request", Action: "closed", Merged: true,
		PRNumber: 43, RepoFullName: "owner/repo", MergeCommitSHA: "e7ddb9e",
	}}
	w := httptest.NewRecorder()
	newWebhookServerWithConfig(mockSCM, c, zerolog.Nop(), true).Handler()(w,
		httptest.NewRequest(http.MethodPost, "/webhook/scm", bytes.NewReader([]byte(`{}`))))
	assert.Equal(t, http.StatusNoContent, w.Code)

	var got v1alpha1.PRStatus
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(prs), &got))
	assert.True(t, got.Status.Merged)
	assert.Equal(t, "e7ddb9e", got.Status.MergeCommitSHA)
	assert.Equal(t, int64(2), got.Status.ObservedGeneration)
	assert.False(t, got.Status.ClosedFinal)
	assert.Empty(t, got.Status.PollError)
	assert.False(t, got.Status.Approved)
	assert.Zero(t, got.Status.ApprovalCount)
}

// TestWebhook_ConfirmsMergeWithSCM covers B73: a validly signed merge event
// marks the PRStatus merged only when the SCM provider, asked once, reports
// the PR merged. When it reports the PR not merged, or the call fails, the
// event gets 204 and the PRStatus is left to polling. An event that names no
// tracked PR, or one already marked, makes no call.
func TestWebhook_ConfirmsMergeWithSCM(t *testing.T) {
	open := v1alpha1.PRStatusStatus{Open: true}
	tests := []struct {
		name       string
		status     v1alpha1.PRStatusStatus
		pr         int
		notMerged  bool
		statusErr  error
		wantMerged bool
		wantCalls  int
	}{
		{name: "the provider reports the PR merged", status: open, pr: 42, wantMerged: true, wantCalls: 1},
		{name: "the provider reports the PR not merged", status: open, pr: 42, notMerged: true, wantCalls: 1},
		{name: "the provider call fails", status: open, pr: 42, statusErr: errors.New("status 502: Bad Gateway"), wantCalls: 1},
		{name: "no PRStatus tracks the PR", status: open, pr: 99, wantCalls: 0},
		{name: "already marked merged", status: v1alpha1.PRStatusStatus{Merged: true, MergeCommitSHA: "d7d4d8a"}, pr: 42,
			wantMerged: true, wantCalls: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prs := &v1alpha1.PRStatus{
				ObjectMeta: metav1.ObjectMeta{Name: "prstatus-bundle-1-prod", Namespace: "default"},
				Spec:       v1alpha1.PRStatusSpec{PRURL: "https://github.com/owner/repo/pull/42", PRNumber: 42, Repo: "owner/repo"},
				Status:     tc.status,
			}
			c := fake.NewClientBuilder().WithScheme(webhookScheme()).
				WithObjects(prs).WithStatusSubresource(prs).Build()
			mockSCM := &mockSCMProvider{event: scm.WebhookEvent{
				EventType: "pull_request", Action: "closed", Merged: true,
				PRNumber: tc.pr, RepoFullName: "owner/repo", MergeCommitSHA: "e7ddb9e",
			}, notMerged: tc.notMerged, statusErr: tc.statusErr}
			w := httptest.NewRecorder()
			newWebhookServerWithConfig(mockSCM, c, zerolog.Nop(), true).Handler()(w,
				httptest.NewRequest(http.MethodPost, "/webhook/scm", bytes.NewReader([]byte(`{}`))))
			assert.Equal(t, http.StatusNoContent, w.Code)
			assert.Equal(t, tc.wantCalls, mockSCM.statusCalls, "GetPRStatus calls")

			var got v1alpha1.PRStatus
			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(prs), &got))
			assert.Equal(t, tc.wantMerged, got.Status.Merged)
			if !tc.wantMerged {
				assert.Equal(t, tc.status, got.Status, "the status is left to polling")
			}
		})
	}
}

// TestWebhook_MergeConfirmationIsBounded covers the B73 follow-up: the SCM
// call that confirms a merge event runs under its own deadline, within the 10
// seconds GitHub gives a delivery (mergeConfirmTimeout), not the handler's
// 30-second context or the provider's 30-second HTTP timeout, which would
// turn every slow confirmation into a failed delivery. An SCM that does not
// answer in time gets the same treatment as a failed call: the event is
// answered 204 and counted as received (eventsProcessed and mergedPREvents
// count events, not confirmed merges), the PRStatus is left alone, and
// polling records the merge.
func TestWebhook_MergeConfirmationIsBounded(t *testing.T) {
	open := v1alpha1.PRStatusStatus{Open: true}
	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-bundle-1-prod", Namespace: "default"},
		Spec:       v1alpha1.PRStatusSpec{PRURL: "https://github.com/owner/repo/pull/42", PRNumber: 42, Repo: "owner/repo"},
		Status:     open,
	}
	c := fake.NewClientBuilder().WithScheme(webhookScheme()).WithObjects(prs).WithStatusSubresource(prs).Build()
	mockSCM := &mockSCMProvider{event: scm.WebhookEvent{
		EventType: "pull_request", Action: "closed", Merged: true, PRNumber: 42, RepoFullName: "owner/repo",
	}, hang: true}
	var logs bytes.Buffer
	s := newWebhookServerWithConfig(mockSCM, c, zerolog.New(&logs), true)

	w := httptest.NewRecorder()
	before := time.Now()
	s.Handler()(w, httptest.NewRequest(http.MethodPost, "/webhook/scm", bytes.NewReader([]byte(`{}`))))

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, 1, mockSCM.statusCalls, "GetPRStatus calls")
	require.True(t, mockSCM.statusHasDeadline, "the confirmation call has no deadline of its own")
	left := mockSCM.statusDeadline.Sub(before)
	assert.Greater(t, left, time.Duration(0))
	assert.LessOrEqual(t, left, 10*time.Second, "GitHub counts a delivery failed after 10 seconds")
	assert.Equal(t, int64(1), s.eventsTotal.Load(), "the event is counted as received")
	assert.Equal(t, int64(1), s.mergedPREventsTotal.Load(),
		"mergedPREvents counts the merged-PR events received, confirmed or not")
	assert.NotNil(t, webhookLogLine(t, &logs,
		"could not confirm the merge event with the SCM provider; PRStatus not marked merged, polling will record the merge"))

	var got v1alpha1.PRStatus
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(prs), &got))
	assert.Equal(t, open, got.Status, "the status is left to polling")
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

// prAPI serves the pull request API a real provider asks before the webhook
// marks a merge (B73): GET of PR (merge request) pr answers with the state
// provider reports for a merged PR, or for an open one when open is set. It
// returns the API URL for scm.NewProvider.
func prAPI(t *testing.T, provider string, pr int, open bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/"+strconv.Itoa(pr)) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, prJSON(provider, open))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// prJSON is provider's pull request, merged or open, as its API returns it.
func prJSON(provider string, open bool) string {
	switch provider {
	case "gitlab":
		if open {
			return `{"state":"opened"}`
		}
		return `{"state":"merged"}`
	case "bitbucket":
		if open {
			return `{"state":"OPEN"}`
		}
		return `{"state":"MERGED"}`
	case "azuredevops":
		if open {
			return `{"status":"active"}`
		}
		return `{"status":"completed"}`
	}
	if open {
		return `{"state":"open","merged":false}`
	}
	return `{"state":"closed","merged":true}`
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
			p, err := scm.NewProvider("github", "", prAPI(t, "github", 7, false), tt.secret)
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
			p, err := scm.NewProvider(tc.provider, "", prAPI(t, tc.provider, 5, false), secret)
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

// TestWebhook_PRStatusDeletedBeforePatch: a PRStatus deleted between the list
// and the merged patch is skipped. The event is still accepted (204), with no
// error log: a deleted PRStatus has no step left to advance.
func TestWebhook_PRStatusDeletedBeforePatch(t *testing.T) {
	prs := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-bundle-1-prod", Namespace: "default"},
		Spec:       v1alpha1.PRStatusSpec{PRURL: "https://github.com/owner/repo/pull/42", PRNumber: 42, Repo: "owner/repo"},
		Status:     v1alpha1.PRStatusStatus{Open: true},
	}
	c := fake.NewClientBuilder().WithScheme(webhookScheme()).WithObjects(prs).WithStatusSubresource(prs).
		WithInterceptorFuncs(objectgonetest.DeleteOnWrite(t, nil)).Build()
	mockSCM := &mockSCMProvider{event: scm.WebhookEvent{
		EventType: "pull_request", Action: "closed", Merged: true, PRNumber: 42, RepoFullName: "owner/repo",
	}}
	var logs bytes.Buffer
	handler := newWebhookServerWithConfig(mockSCM, c, zerolog.New(&logs), true).Handler()

	body := []byte(`{"action":"closed","pull_request":{"number":42,"merged":true},"repository":{"full_name":"owner/repo"}}`)
	req := httptest.NewRequest(http.MethodPost, "/webhook/scm", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.NotContains(t, logs.String(), `"level":"error"`)
}

// webhookLogLine returns the JSON log line with message msg, or nil.
func webhookLogLine(t *testing.T, logs *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var l map[string]any
		if json.Unmarshal([]byte(line), &l) == nil && l["message"] == msg {
			return l
		}
	}
	return nil
}

// TestWebhook_LogFields checks the webhook logs say where an event came from:
// "webhook received" names the repo, "PRStatus marked merged via webhook" the
// PRStatus namespace, and a refused event the header its signature was read
// from (the one scm.WebhookSignature reads, "none" without one) and the remote
// address, never the signature or token itself. The 401 stays a text error,
// like the handler's other errors (B76).
func TestWebhook_LogFields(t *testing.T) {
	const secret = "s3cret"
	const remote = "203.0.113.7:4711"
	body := `{"action":"closed","number":5,"pull_request":{"number":5,"merged":true},"repository":{"full_name":"o/r"}}`

	t.Run("accepted merge", func(t *testing.T) {
		p, err := scm.NewProvider("github", "", prAPI(t, "github", 5, false), secret)
		require.NoError(t, err)
		c := fake.NewClientBuilder().WithScheme(webhookScheme()).
			WithObjects(webhookPRS("prs", "team-a", "o/r", 5)).
			WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
		var logs bytes.Buffer
		req := httptest.NewRequest(http.MethodPost, "/webhook/scm", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-Hub-Signature-256", "sha256="+hmacHexOf(secret, body))
		w := httptest.NewRecorder()
		newWebhookServerWithConfig(p, c, zerolog.New(&logs), true).Handler()(w, req)
		require.Equal(t, http.StatusNoContent, w.Code, logs.String())

		received := webhookLogLine(t, &logs, "webhook received")
		require.NotNil(t, received, logs.String())
		assert.Equal(t, "o/r", received["repo"])
		marked := webhookLogLine(t, &logs, "PRStatus marked merged via webhook")
		require.NotNil(t, marked, logs.String())
		assert.Equal(t, "prs", marked["prstatus"])
		assert.Equal(t, "team-a", marked["namespace"])
	})

	tests := []struct {
		name       string
		provider   string
		headers    map[string]string
		wantHeader string
	}{
		{"github wrong signature", "github",
			map[string]string{"X-Hub-Signature-256": "sha256=" + hmacHexOf("wrong-secret-value", body)}, "X-Hub-Signature-256"},
		{"gitlab wrong token", "gitlab",
			map[string]string{"X-Gitlab-Token": "wrong-secret-value"}, "X-Gitlab-Token"},
		{"azure devops wrong token", "azuredevops",
			map[string]string{"X-AzureDevOps-Token": "wrong-secret-value"}, "X-AzureDevOps-Token"},
		{"gitea signature read from X-Hub-Signature-256 first", "gitea",
			map[string]string{"X-Gitea-Signature": hmacHexOf("wrong-secret-value", body), "X-Hub-Signature-256": "sha256=" + hmacHexOf("wrong-secret-value", body)},
			"X-Hub-Signature-256"},
		{"unsigned", "forgejo", map[string]string{}, "none"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := scm.NewProvider(tc.provider, "", prAPI(t, tc.provider, 5, false), secret)
			require.NoError(t, err)
			c := fake.NewClientBuilder().WithScheme(webhookScheme()).
				WithObjects(webhookPRS("prs", "team-a", "o/r", 5)).
				WithStatusSubresource(&v1alpha1.PRStatus{}).Build()
			var logs bytes.Buffer
			req := httptest.NewRequest(http.MethodPost, "/webhook/scm", strings.NewReader(body))
			req.RemoteAddr = remote
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			newWebhookServerWithConfig(p, c, zerolog.New(&logs), true).Handler()(w, req)

			require.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"), "the 401 is a text error like the others")
			refused := webhookLogLine(t, &logs, "webhook signature invalid or parse error")
			require.NotNil(t, refused, logs.String())
			assert.Equal(t, tc.wantHeader, refused["signatureHeader"])
			assert.Equal(t, remote, refused["remoteAddr"])
			assert.NotContains(t, logs.String(), "wrong-secret-value", "a signature or token is never logged")
			assert.NotContains(t, logs.String(), hmacHexOf("wrong-secret-value", body), "a signature is never logged")
			assert.False(t, webhookMerged(t, c, "prs", "team-a"))
		})
	}
}

func hmacHexOf(key, body string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}
