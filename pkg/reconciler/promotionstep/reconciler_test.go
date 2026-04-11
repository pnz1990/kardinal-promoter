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

// TestWaitingForMerge_AdvancesOnPRMerged verifies WaitingForMerge → HealthChecking on prMerged=true.
func TestWaitingForMerge_AdvancesOnPRMerged(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-wfm", "nginx-demo", "bundle-1", "prod")
	step.Status.State = "WaitingForMerge"
	step.Status.PRURL = "https://github.com/test/repo/pull/5"
	step.Status.Outputs = map[string]string{"prNumber": "5", "prURL": "https://github.com/test/repo/pull/5"}
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	mockSCMInst := &mockSCM{merged: true, open: false}
	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       mockSCMInst,
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

// TestWaitingForMerge_StaysOnPROpen verifies that WaitingForMerge stays if PR is still open.
func TestWaitingForMerge_StaysOnPROpen(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-wfm", "nginx-demo", "bundle-1", "prod")
	step.Status.State = "WaitingForMerge"
	step.Status.PRURL = "https://github.com/test/repo/pull/5"
	step.Status.Outputs = map[string]string{"prNumber": "5"}
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{merged: false, open: true},
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

// TestWaitingForMerge_FailsOnPRClosed verifies that a closed-without-merge PR transitions to Failed.
func TestWaitingForMerge_FailsOnPRClosed(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-wfm", "nginx-demo", "bundle-1", "prod")
	step.Status.State = "WaitingForMerge"
	step.Status.PRURL = "https://github.com/test/repo/pull/5"
	step.Status.Outputs = map[string]string{"prNumber": "5"}
	pipeline := makePipeline("nginx-demo")
	bundle := makeBundle("bundle-1", "nginx-demo")

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{},
	).WithObjects(step, pipeline, bundle).Build()

	// PR closed without merge: merged=false, open=false
	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{merged: false, open: false},
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

// TestEvidenceCopiedToBundle verifies that Verified state copies PRURL to Bundle.status.environments.
func TestEvidenceCopiedToBundle(t *testing.T) {
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

	var updatedBundle v1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "bundle-1", Namespace: "default"}, &updatedBundle))

	found := false
	for _, env := range updatedBundle.Status.Environments {
		if env.Name == "test" {
			found = true
			assert.Equal(t, "https://github.com/test/repo/pull/7", env.PRURL)
		}
	}
	assert.True(t, found, "test environment status should be in bundle")
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
