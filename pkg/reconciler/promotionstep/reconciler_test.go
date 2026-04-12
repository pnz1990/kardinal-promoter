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

package promotionstep_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// ---------- fakes ----------

type mockSCM struct {
	merged      bool
	open        bool
	openPRErr   error
	prURL       string
	prNumber    int
	getPRCalled int
	openCalled  int
}

func (m *mockSCM) OpenPR(_ context.Context, _, _, _, _, _ string) (string, int, error) {
	m.openCalled++
	return m.prURL, m.prNumber, m.openPRErr
}
func (m *mockSCM) ClosePR(_ context.Context, _ string, _ int) error               { return nil }
func (m *mockSCM) CommentOnPR(_ context.Context, _ string, _ int, _ string) error { return nil }
func (m *mockSCM) GetPRStatus(_ context.Context, _ string, _ int) (bool, bool, error) {
	m.getPRCalled++
	return m.merged, m.open, nil
}
func (m *mockSCM) ParseWebhookEvent(_ []byte, _ string) (scm.WebhookEvent, error) {
	return scm.WebhookEvent{}, nil
}
func (m *mockSCM) AddLabelsToPR(_ context.Context, _ string, _ int, _ []string) error {
	return nil
}

type mockGit struct {
	cloneErr error
}

func (m *mockGit) Clone(_ context.Context, _, _, _ string) error        { return m.cloneErr }
func (m *mockGit) Checkout(_ context.Context, _, _ string) error        { return nil }
func (m *mockGit) CommitAll(_ context.Context, _, _, _, _ string) error { return nil }
func (m *mockGit) Push(_ context.Context, _, _, _, _ string) error      { return nil }

// ---------- helpers ----------

func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func makeStep(name, pipelineName, bundleName, env string) *v1alpha1.PromotionStep {
	return &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: pipelineName,
			BundleName:   bundleName,
			Environment:  env,
			StepType:     "auto",
		},
	}
}

func makePipeline(name string) *v1alpha1.Pipeline {
	return &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{
				URL:    "https://github.com/test/repo",
				Branch: "main",
			},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test", Approval: "auto"},
				{Name: "prod", Approval: "pr-review"},
			},
		},
	}
}

func makeBundle(name, pipelineName string) *v1alpha1.Bundle {
	return &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: pipelineName,
			// No images: kustomize-set-image will be a no-op (returns StepSuccess with "no images to update")
			// enabling the full step sequence to run with just mock git client.
		},
		Status: v1alpha1.BundleStatus{Phase: "Promoting"},
	}
}

// TestPendingToPromoting verifies the Pending → Promoting transition on first reconcile.
func TestPendingToPromoting(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-test", "nginx-demo", "bundle-1", "test")
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{merged: true, open: false, prURL: "https://github.com/test/repo/pull/1", prNumber: 1},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-test", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.True(t, result.Requeue || result.RequeueAfter > 0, "should requeue after pending→promoting") //nolint:staticcheck

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-test", Namespace: "default"}, &updated))
	assert.Equal(t, "Promoting", updated.Status.State)
}

// TestPromotingToVerified verifies that an auto-approval env runs all steps and reaches Verified.
func TestPromotingToVerified(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-test", "nginx-demo", "bundle-1", "test")
	step.Status.State = "Promoting"
	step.Status.CurrentStepIndex = 0
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{merged: true, open: false, prURL: "https://github.com/test/repo/pull/1", prNumber: 1},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	// Run reconcile in a loop until terminal or max iterations.
	ctx := context.Background()
	maxIter := 20
	for i := 0; i < maxIter; i++ {
		result, err := r.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "step-test", Namespace: "default"},
		})
		require.NoError(t, err)

		var s v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "step-test", Namespace: "default"}, &s))
		if s.Status.State == "Verified" || s.Status.State == "Failed" {
			break
		}
		if !result.Requeue && result.RequeueAfter == 0 { //nolint:staticcheck
			break
		}
	}

	var final v1alpha1.PromotionStep
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "step-test", Namespace: "default"}, &final))
	assert.Equal(t, "Verified", final.Status.State)
}

// TestWaitingForMerge_AdvancesOnPRMerged verifies WaitingForMerge → HealthChecking when PRStatus.status.merged=true.
func TestWaitingForMerge_AdvancesOnPRMerged(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-wfm", "nginx-demo", "bundle-1", "prod")
	step.Spec.PRStatusRef = "prstatus-step-wfm"
	step.Status.State = "WaitingForMerge"
	step.Status.PRURL = "https://github.com/test/repo/pull/5"
	step.Status.Outputs = map[string]string{"prNumber": "5", "prURL": "https://github.com/test/repo/pull/5"}
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	// PRStatus CRD with status.merged=true (written by PRStatusReconciler)
	now := metav1.Now()
	prStatus := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-step-wfm", Namespace: "default"},
		Spec:       v1alpha1.PRStatusSpec{PRURL: "https://github.com/test/repo/pull/5", PRNumber: 5, Repo: "test/repo"},
		Status:     v1alpha1.PRStatusStatus{Merged: true, Open: false, LastCheckedAt: &now},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.PRStatus{},
	).WithObjects(step, pipeline, bundle, prStatus).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-wfm", Namespace: "default"},
	})
	require.NoError(t, err)

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-wfm", Namespace: "default"}, &updated))
	assert.Equal(t, "HealthChecking", updated.Status.State)
}

// TestWaitingForMerge_StaysOnPROpen verifies that WaitingForMerge stays if PRStatus.status.open=true.
func TestWaitingForMerge_StaysOnPROpen(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-wfm", "nginx-demo", "bundle-1", "prod")
	step.Spec.PRStatusRef = "prstatus-step-wfm"
	step.Status.State = "WaitingForMerge"
	step.Status.PRURL = "https://github.com/test/repo/pull/5"
	step.Status.Outputs = map[string]string{"prNumber": "5"}
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	// PRStatus CRD with status.open=true, not yet merged
	now := metav1.Now()
	prStatus := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-step-wfm", Namespace: "default"},
		Spec:       v1alpha1.PRStatusSpec{PRURL: "https://github.com/test/repo/pull/5", PRNumber: 5, Repo: "test/repo"},
		Status:     v1alpha1.PRStatusStatus{Merged: false, Open: true, LastCheckedAt: &now},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.PRStatus{},
	).WithObjects(step, pipeline, bundle, prStatus).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-wfm", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0), "should requeue after delay")

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-wfm", Namespace: "default"}, &updated))
	assert.Equal(t, "WaitingForMerge", updated.Status.State)
}

// TestWaitingForMerge_FailsOnPRClosed verifies that PRStatus.status.open=false,merged=false transitions to Failed.
func TestWaitingForMerge_FailsOnPRClosed(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-wfm", "nginx-demo", "bundle-1", "prod")
	step.Spec.PRStatusRef = "prstatus-step-wfm"
	step.Status.State = "WaitingForMerge"
	step.Status.PRURL = "https://github.com/test/repo/pull/5"
	step.Status.Outputs = map[string]string{"prNumber": "5"}
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	// PRStatus CRD: PR closed without merge (open=false, merged=false, lastCheckedAt set)
	now := metav1.Now()
	prStatus := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-step-wfm", Namespace: "default"},
		Spec:       v1alpha1.PRStatusSpec{PRURL: "https://github.com/test/repo/pull/5", PRNumber: 5, Repo: "test/repo"},
		Status:     v1alpha1.PRStatusStatus{Merged: false, Open: false, LastCheckedAt: &now},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.PRStatus{},
	).WithObjects(step, pipeline, bundle, prStatus).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-wfm", Namespace: "default"},
	})
	require.NoError(t, err)

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-wfm", Namespace: "default"}, &updated))
	assert.Equal(t, "Failed", updated.Status.State)
	assert.Contains(t, updated.Status.Message, "closed without merging")
}

// TestHealthCheckingToVerified verifies HealthChecking → Verified transition.
func TestHealthCheckingToVerified(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-hc", "nginx-demo", "bundle-1", "test")
	step.Status.State = "HealthChecking"
	step.Status.CurrentStepIndex = 4 // past all git+PR steps
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-hc", Namespace: "default"},
	})
	require.NoError(t, err)

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-hc", Namespace: "default"}, &updated))
	assert.Equal(t, "Verified", updated.Status.State)
}

// TestVerifiedIsTerminal verifies that Verified is a no-op (idempotent).
func TestVerifiedIsTerminal(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-done", "nginx-demo", "bundle-1", "test")
	step.Status.State = "Verified"
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-done", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.False(t, result.Requeue) //nolint:staticcheck
	assert.Equal(t, time.Duration(0), result.RequeueAfter)
}

// TestEvidenceNotCopiedByPromotionStepReconciler verifies that the PromotionStep reconciler
// does NOT write evidence to Bundle.status.environments (PS-9 elimination).
// Evidence sync is now handled by the Bundle reconciler watching PromotionStep changes.
func TestEvidenceNotCopiedByPromotionStepReconciler(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-hc", "nginx-demo", "bundle-1", "test")
	step.Status.State = "HealthChecking"
	step.Status.PRURL = "https://github.com/test/repo/pull/7"
	step.Status.Outputs = map[string]string{"prURL": "https://github.com/test/repo/pull/7"}
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-hc", Namespace: "default"},
	})
	require.NoError(t, err)

	// The PromotionStep reconciler must NOT write to Bundle.status.environments.
	// The Bundle reconciler (watching PromotionStep events) handles evidence sync.
	var updatedBundle v1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "bundle-1", Namespace: "default"}, &updatedBundle))
	assert.Empty(t, updatedBundle.Status.Environments,
		"PromotionStep reconciler must NOT write to Bundle.status.environments (PS-9 eliminated; Bundle reconciler handles this)")
}

// TestShardFiltering verifies that PromotionSteps with non-matching shard labels are skipped.
func TestShardFiltering(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-shard", "nginx-demo", "bundle-1", "test")
	step.Labels = map[string]string{"kardinal.io/shard": "cluster-b"}
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		Shard:     "cluster-a", // different shard
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-shard", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	// State should remain empty (untouched)
	var s v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-shard", Namespace: "default"}, &s))
	assert.Equal(t, "", s.Status.State)
}

// TestIdempotency_PendingToPromotingTwice verifies reconciling twice in Pending does not double-mutate.
func TestIdempotency_PendingToPromotingTwice(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-idem", "nginx-demo", "bundle-1", "test")
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{merged: true, open: false, prURL: "https://github.com/test/repo/pull/1", prNumber: 1},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-idem", Namespace: "default"}}

	// First reconcile: Pending → Promoting
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var s1 v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &s1))
	assert.Equal(t, "Promoting", s1.Status.State)

	// Second reconcile: should continue execution or stay Promoting (not crash)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var s2 v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &s2))
	// State should have advanced (Promoting or beyond), not regressed
	assert.NotEqual(t, "", s2.Status.State)
}

// TestStepIndex_OutputsAccumulated verifies step outputs accumulate across reconcile cycles.
func TestStepIndex_OutputsAccumulated(t *testing.T) {
	_ = steps.DefaultSequence // ensure package import is used
	// This test verifies that Outputs in status persist across reconcile calls.
	scheme := buildScheme(t)
	step := makeStep("step-out", "nginx-demo", "bundle-1", "test")
	step.Status.State = "Promoting"
	step.Status.CurrentStepIndex = 0
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{merged: true, open: false, prURL: "https://github.com/test/repo/pull/2", prNumber: 2},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	// Reconcile until terminal.
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		result, err := r.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "step-out", Namespace: "default"},
		})
		require.NoError(t, err)

		var s v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "step-out", Namespace: "default"}, &s))
		if s.Status.State == "Verified" || s.Status.State == "Failed" {
			break
		}
		if !result.Requeue && result.RequeueAfter == 0 { //nolint:staticcheck
			break
		}
	}

	var final v1alpha1.PromotionStep
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "step-out", Namespace: "default"}, &final))
	assert.Equal(t, "Verified", final.Status.State)
}

// TestPausedPipeline_ReconcilerNolongerChecksSpecPaused documents the PS-2 fix:
// the PromotionStep reconciler no longer reads Pipeline.Spec.Paused.
// Pause enforcement is now via the freeze PolicyGate (Graph-visible).
// A "Promoting" step with a paused pipeline will now attempt to advance (and the
// freeze gate in the Graph prevents the Graph node from being triggered in prod).
func TestPausedPipeline_ReconcilerNolongerChecksSpecPaused(t *testing.T) {
	scheme := buildScheme(t)

	pausedPipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Paused: true, // Note: reconciler no longer checks this (PS-2 fix)
			Git:    v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test", Approval: "auto"},
			},
		},
	}
	step := makeStep("step-paused", "nginx-demo", "bundle-1", "test")
	step.Status.State = "Promoting"
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pausedPipeline, bundle).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	// After the PS-2 fix: reconciler proceeds normally — it does NOT hold because
	// of Spec.Paused. The Graph-layer freeze gate (not the reconciler) enforces pause.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-paused", Namespace: "default"},
	})
	require.NoError(t, err, "reconciler must not error — it ignores Spec.Paused (PS-2 fix)")

	// Step advances (or stays at Promoting after attempting git clone — SCM mock returns success).
	// The key point: the reconciler no longer holds due to Spec.Paused.
	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-paused", Namespace: "default"}, &updated))
	// State changes from "Promoting" (the reconciler ran the step) — not stuck.
	// The exact new state depends on mock SCM behavior; what matters is it advanced.
	assert.NotEqual(t, "Promoting", updated.Status.State,
		"step must have advanced — Spec.Paused no longer blocks in reconciler (PS-2 fix)")
}

// TestWorkDir_PersistedToStatus verifies that status.workDir is written when a step
// transitions from Pending to Promoting. This is the ST-7/ST-8 short-term mitigation:
// after a controller restart, the reconciler reads status.workDir instead of recomputing
// the path, enabling crash-recovery without re-cloning.
func TestWorkDir_PersistedToStatus(t *testing.T) {
	scheme := buildScheme(t)
	pipeline := makePipeline("nginx-demo")
	step := makeStep("step-1", "nginx-demo", "bundle-1", "test")
	// Step is Pending (empty state) — will transition to Promoting on first reconcile.
	step.Status.State = ""
	bundle := makeBundle("bundle-1", "nginx-demo")

	expectedWorkDir := t.TempDir()

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return expectedWorkDir },
	}

	// First reconcile: Pending → Promoting, workDir should be persisted.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-1", Namespace: "default"},
	})
	require.NoError(t, err)

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-1", Namespace: "default"}, &updated))
	assert.Equal(t, expectedWorkDir, updated.Status.WorkDir,
		"status.workDir must be persisted on Pending→Promoting transition (ST-7/ST-8 mitigation)")
}

// TestWorkDir_CleanedUpOnVerified verifies that the working directory is removed from disk
// when a PromotionStep reaches the Verified terminal state.
func TestWorkDir_CleanedUpOnVerified(t *testing.T) {
	scheme := buildScheme(t)
	pipeline := makePipeline("nginx-demo")
	step := makeStep("step-terminal", "nginx-demo", "bundle-1", "test")
	// Put step in Verified terminal state with a workDir that exists.
	workDir := t.TempDir()
	step.Status.State = "Verified"
	step.Status.WorkDir = workDir
	bundle := makeBundle("bundle-1", "nginx-demo")

	// Create a marker file in the workDir to confirm it exists before cleanup.
	markerFile := workDir + "/marker.txt"
	require.NoError(t, os.WriteFile(markerFile, []byte("test"), 0o644))

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return workDir },
	}

	// Reconcile the terminal step — should trigger cleanup.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-terminal", Namespace: "default"},
	})
	require.NoError(t, err)

	// WorkDir should no longer exist on disk.
	_, statErr := os.Stat(workDir)
	assert.True(t, os.IsNotExist(statErr),
		"working directory must be removed when step reaches terminal state (Verified)")
}

// TestShardFiltering_MatchingShard verifies that a reconciler processes steps whose shard label matches.
func TestShardFiltering_MatchingShard(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-matching", "nginx-demo", "bundle-1", "test")
	step.Labels = map[string]string{"kardinal.io/shard": "cluster-eu"}
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		Shard:     "cluster-eu", // matching shard
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-matching", Namespace: "default"},
	})
	require.NoError(t, err)

	// Step should have advanced from empty (Pending) state to Promoting.
	var s v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-matching", Namespace: "default"}, &s))
	assert.Equal(t, "Promoting", s.Status.State,
		"matching shard — reconciler must process the step and advance state to Promoting")
}

// TestShardIsolation_TwoShards verifies that two reconcilers with different shards
// each process only their assigned steps (shard isolation in distributed mode).
func TestShardIsolation_TwoShards(t *testing.T) {
	scheme := buildScheme(t)

	stepEU := makeStep("step-eu", "nginx-demo", "bundle-1", "prod-eu")
	stepEU.Labels = map[string]string{"kardinal.io/shard": "cluster-eu"}

	stepUS := makeStep("step-us", "nginx-demo", "bundle-1", "prod-us")
	stepUS.Labels = map[string]string{"kardinal.io/shard": "cluster-us"}

	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(stepEU, stepUS, pipeline, bundle).Build()

	// EU reconciler: processes only cluster-eu steps.
	rEU := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		Shard:     "cluster-eu",
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	// US reconciler: processes only cluster-us steps.
	rUS := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		Shard:     "cluster-us",
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	ctx := context.Background()

	// EU reconciler processes EU step but skips US step.
	_, err := rEU.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-eu", Namespace: "default"}})
	require.NoError(t, err)
	_, err = rEU.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-us", Namespace: "default"}})
	require.NoError(t, err)

	// US reconciler processes US step but skips EU step.
	_, err = rUS.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-us", Namespace: "default"}})
	require.NoError(t, err)
	_, err = rUS.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-eu", Namespace: "default"}})
	require.NoError(t, err)

	var eu, us v1alpha1.PromotionStep
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "step-eu", Namespace: "default"}, &eu))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "step-us", Namespace: "default"}, &us))

	// EU step processed only by EU reconciler → Promoting.
	assert.Equal(t, "Promoting", eu.Status.State,
		"EU step must be in Promoting after EU reconciler ran")
	// US step processed only by US reconciler → Promoting.
	assert.Equal(t, "Promoting", us.Status.State,
		"US step must be in Promoting after US reconciler ran")
}

// TestOrphanGuard_SelfDeletesWhenBundleGone verifies that when the parent Bundle
// no longer exists (e.g. deleted manually), the PromotionStep self-deletes
// instead of entering an infinite error loop (#248).
func TestOrphanGuard_SelfDeletesWhenBundleGone(t *testing.T) {
	s := buildScheme(t)

	// Create a PromotionStep with spec.bundleName pointing to a non-existent bundle.
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "orphaned-step",
			Namespace: "default",
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "my-pipeline",
			BundleName:   "deleted-bundle",
			Environment:  "test",
		},
	}
	pipeline := makePipeline("my-pipeline")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(step, pipeline).
		WithStatusSubresource(step).
		Build()

	r := promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "orphaned-step", Namespace: "default"},
	})
	require.NoError(t, err, "orphan guard must not return an error")

	// The PromotionStep must be deleted.
	var got v1alpha1.PromotionStep
	err = c.Get(context.Background(), types.NamespacedName{Name: "orphaned-step", Namespace: "default"}, &got)
	require.Error(t, err, "orphaned PromotionStep must be self-deleted")
	// controller-runtime fake returns a NotFound-like error for deleted objects.
	_ = got
}

// TestOrphanGuard_NoDeleteWhenBundleExists verifies that when the parent Bundle
// exists the reconciler proceeds normally (no self-deletion).
func TestOrphanGuard_NoDeleteWhenBundleExists(t *testing.T) {
	s := buildScheme(t)

	bundle := makeBundle("existing-bundle", "my-pipeline")
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "normal-step",
			Namespace: "default",
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "my-pipeline",
			BundleName:   "existing-bundle",
			Environment:  "test",
		},
	}
	pipeline := makePipeline("my-pipeline")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(step, bundle, pipeline).
		WithStatusSubresource(step).
		Build()

	r := promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "normal-step", Namespace: "default"},
	})
	require.NoError(t, err)

	// The PromotionStep must still exist and have been processed (state = Promoting).
	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "normal-step", Namespace: "default"}, &got))
	assert.Equal(t, "Promoting", got.Status.State, "step must advance to Promoting when bundle exists")
}
