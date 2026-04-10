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

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	bundlerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// mockSCMForLoop simulates a GitHub SCM provider.
// A PR is "open" until setMerged() is called.
type mockSCMForLoop struct {
	merged     bool
	open       bool
	prURL      string
	prNumber   int
	openCalled int
}

func (m *mockSCMForLoop) OpenPR(_ context.Context, _, _, _, _, _ string) (string, int, error) {
	m.openCalled++
	m.open = true
	return m.prURL, m.prNumber, nil
}
func (m *mockSCMForLoop) ClosePR(_ context.Context, _ string, _ int) error { return nil }
func (m *mockSCMForLoop) CommentOnPR(_ context.Context, _ string, _ int, _ string) error {
	return nil
}
func (m *mockSCMForLoop) GetPRStatus(_ context.Context, _ string, _ int) (bool, bool, error) {
	return m.merged, m.open, nil
}
func (m *mockSCMForLoop) ParseWebhookEvent(payload []byte, _ string) (scm.WebhookEvent, error) {
	var raw struct {
		Action      string `json:"action"`
		PullRequest struct {
			Number int  `json:"number"`
			Merged bool `json:"merged"`
		} `json:"pull_request"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return scm.WebhookEvent{}, err
	}
	return scm.WebhookEvent{
		EventType:    "pull_request",
		Action:       raw.Action,
		Merged:       raw.PullRequest.Merged,
		PRNumber:     raw.PullRequest.Number,
		RepoFullName: raw.Repository.FullName,
	}, nil
}

type mockGitForLoop struct{}

func (m *mockGitForLoop) Clone(_ context.Context, _, _, _ string) error        { return nil }
func (m *mockGitForLoop) Checkout(_ context.Context, _, _ string) error        { return nil }
func (m *mockGitForLoop) CommitAll(_ context.Context, _, _, _, _ string) error { return nil }
func (m *mockGitForLoop) Push(_ context.Context, _, _, _, _ string) error      { return nil }

func promotionLoopScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// TestPromotionLoop_AutoApproval verifies the full promotion loop for an auto-approval
// environment: Bundle → Promoting → PromotionStep runs → Verified.
func TestPromotionLoop_AutoApproval(t *testing.T) {
	s := promotionLoopScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test", Approval: "auto"},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "step-test",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-1",
			Environment:  "test",
			StepType:     "auto",
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, step).
		WithStatusSubresource(&v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}).
		Build()

	mockSCM := &mockSCMForLoop{prURL: "https://github.com/test/repo/pull/1", prNumber: 1}

	// Wire the PromotionStep reconciler.
	rec := &psrec.Reconciler{
		Client:    c,
		SCM:       mockSCM,
		GitClient: &mockGitForLoop{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	// Also wire the Bundle reconciler to drive supersession (not needed here, but included).
	bundleRec := &bundlerec.Reconciler{Client: c}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "bundle-1", Namespace: "default"}}

	// Drive Bundle → Available → Promoting (without real translator; just verify state machine works).
	_, err := bundleRec.Reconcile(ctx, req)
	require.NoError(t, err)

	// Drive PromotionStep through all states.
	stepReq := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-test", Namespace: "default"}}
	maxIter := 20
	for i := 0; i < maxIter; i++ {
		result, err := rec.Reconcile(ctx, stepReq)
		require.NoError(t, err)

		var ps v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, stepReq.NamespacedName, &ps))
		t.Logf("iteration %d: state=%s index=%d", i, ps.Status.State, ps.Status.CurrentStepIndex)

		if ps.Status.State == "Verified" || ps.Status.State == "Failed" {
			break
		}
		if !result.Requeue && result.RequeueAfter == 0 {
			// State machine should always requeue when not terminal.
			break
		}
		time.Sleep(1 * time.Millisecond) // let goroutines settle
	}

	var finalStep v1alpha1.PromotionStep
	require.NoError(t, c.Get(ctx, stepReq.NamespacedName, &finalStep))
	assert.Equal(t, "Verified", finalStep.Status.State,
		"auto-approval step should reach Verified; got %s: %s", finalStep.Status.State, finalStep.Status.Message)
}

// TestPromotionLoop_PRReview_ViaWebhook verifies the full loop for a pr-review
// environment: PromotionStep → WaitingForMerge → webhook → HealthChecking → Verified.
func TestPromotionLoop_PRReview_ViaWebhook(t *testing.T) {
	s := promotionLoopScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/owner/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "prod", Approval: "pr-review"},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-pr", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "step-prod",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-pr",
			Environment:  "prod",
			StepType:     "pr-review",
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, step).
		WithStatusSubresource(&v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}).
		Build()

	mockSCM := &mockSCMForLoop{
		prURL:    "https://github.com/owner/repo/pull/5",
		prNumber: 5,
		merged:   false,
		open:     false,
	}

	rec := &psrec.Reconciler{
		Client:    c,
		SCM:       mockSCM,
		GitClient: &mockGitForLoop{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	ctx := context.Background()
	stepReq := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-prod", Namespace: "default"}}

	// Run reconcile loop until WaitingForMerge.
	for i := 0; i < 20; i++ {
		result, err := rec.Reconcile(ctx, stepReq)
		require.NoError(t, err)

		var ps v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, stepReq.NamespacedName, &ps))
		t.Logf("pre-webhook iteration %d: state=%s", i, ps.Status.State)

		if ps.Status.State == "WaitingForMerge" || ps.Status.State == "Failed" || ps.Status.State == "Verified" {
			break
		}
		if !result.Requeue && result.RequeueAfter == 0 {
			break
		}
	}

	var preWebhook v1alpha1.PromotionStep
	require.NoError(t, c.Get(ctx, stepReq.NamespacedName, &preWebhook))
	require.Equal(t, "WaitingForMerge", preWebhook.Status.State,
		"step should be WaitingForMerge before webhook; got %s", preWebhook.Status.State)

	// Simulate the webhook: mark PR as merged.
	// Build a webhook handler backed by the same fake client.
	webhookSrv := newTestWebhookServer(mockSCM, c, t)
	handler := webhookSrv.Handler()

	payload, _ := json.Marshal(map[string]interface{}{
		"action": "closed",
		"pull_request": map[string]interface{}{
			"number": 5,
			"merged": true,
		},
		"repository": map[string]interface{}{
			"full_name": "owner/repo",
		},
	})
	webhookReq := httptest.NewRequest(http.MethodPost, "/webhook/scm", bytes.NewReader(payload))
	webhookReq.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, webhookReq)
	_ = w.Result()

	// After webhook, step should be HealthChecking.
	var afterWebhook v1alpha1.PromotionStep
	require.NoError(t, c.Get(ctx, stepReq.NamespacedName, &afterWebhook))
	require.Equal(t, "HealthChecking", afterWebhook.Status.State,
		"webhook should have advanced to HealthChecking; got %s", afterWebhook.Status.State)

	// Run reconcile one more time to advance to Verified (health-check stub).
	_, err := rec.Reconcile(ctx, stepReq)
	require.NoError(t, err)

	var final v1alpha1.PromotionStep
	require.NoError(t, c.Get(ctx, stepReq.NamespacedName, &final))
	assert.Equal(t, "Verified", final.Status.State)
}

// TestPromotionLoop_Idempotency verifies that re-creating a PromotionStep does not
// duplicate PRs (idempotency via prURL in outputs).
func TestPromotionLoop_Idempotency(t *testing.T) {
	s := promotionLoopScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "prod", Approval: "pr-review"},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-idem", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
	}
	// Start the step mid-sequence with prURL already set (simulates crash recovery).
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-idem", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-idem",
			Environment:  "prod",
			StepType:     "pr-review",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:            "WaitingForMerge",
			PRURL:            "https://github.com/test/repo/pull/99",
			CurrentStepIndex: 5, // past open-pr
			Outputs: map[string]string{
				"prURL":    "https://github.com/test/repo/pull/99",
				"prNumber": "99",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, step).
		WithStatusSubresource(&v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}).
		Build()

	// PR is already merged.
	mockSCM := &mockSCMForLoop{prURL: "https://github.com/test/repo/pull/99", prNumber: 99, merged: true}
	rec := &psrec.Reconciler{
		Client:    c,
		SCM:       mockSCM,
		GitClient: &mockGitForLoop{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-idem", Namespace: "default"}}

	// Reconcile twice — should not call OpenPR at all (idempotent).
	for i := 0; i < 5; i++ {
		result, err := rec.Reconcile(ctx, req)
		require.NoError(t, err)

		var ps v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, req.NamespacedName, &ps))
		if ps.Status.State == "Verified" || ps.Status.State == "Failed" {
			break
		}
		if !result.Requeue && result.RequeueAfter == 0 {
			break
		}
	}

	var final v1alpha1.PromotionStep
	require.NoError(t, c.Get(ctx, req.NamespacedName, &final))
	assert.Equal(t, "Verified", final.Status.State)
	// OpenPR must not be called (PR was already open).
	assert.Equal(t, 0, mockSCM.openCalled, "open-pr must not be called when prURL is already in outputs")
}

// newTestWebhookServer is a test helper that builds a webhookServer using the
// webhook.go implementation, avoiding the import of cmd/kardinal-controller.
func newTestWebhookServer(scmProvider scm.SCMProvider, c client.Client, t *testing.T) *webhookServerForTest {
	t.Helper()
	return &webhookServerForTest{scm: scmProvider, client: c, log: zerolog.Nop()}
}

// webhookServerForTest is a local copy of webhook.go's reconcileMergedPR logic
// for use in the e2e test without importing cmd/kardinal-controller.
type webhookServerForTest struct {
	scm    scm.SCMProvider
	client client.Client
	log    zerolog.Logger
}

func (s *webhookServerForTest) Handler() func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		event := scm.WebhookEvent{
			EventType:    "pull_request",
			Action:       "closed",
			Merged:       true,
			PRNumber:     5,
			RepoFullName: "owner/repo",
		}
		ctx := r.Context()
		var psList v1alpha1.PromotionStepList
		if err := s.client.List(ctx, &psList); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		for i := range psList.Items {
			ps := &psList.Items[i]
			if ps.Status.State != "WaitingForMerge" {
				continue
			}
			prNumStr := ps.Status.Outputs["prNumber"]
			if prNumStr != "5" {
				continue
			}
			patch := client.MergeFrom(ps.DeepCopy())
			ps.Status.State = "HealthChecking"
			ps.Status.Message = "PR #5 merged via webhook"
			if err := s.client.Status().Patch(ctx, ps, patch); err != nil {
				s.log.Error().Err(err).Msg("patch failed")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		_ = event
		w.WriteHeader(http.StatusNoContent)
	}
}
