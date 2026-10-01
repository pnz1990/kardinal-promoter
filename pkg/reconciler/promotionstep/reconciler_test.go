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
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
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

	getPRErr  error    // returned by GetPRStatus
	closeErrs []error  // returned in order by successive ClosePR calls; nil entries succeed
	lostClose int      // the first lostClose ClosePR calls close the PR but return an error
	closed    []string // "repo#number" of every ClosePR call
	comments  []string // body of every CommentOnPR call
}

func (m *mockSCM) OpenPR(_ context.Context, _, _, _, _, _ string) (string, int, error) {
	m.openCalled++
	return m.prURL, m.prNumber, m.openPRErr
}

// ClosePR records the call. A close that succeeds, or whose response is lost
// (lostClose), leaves the PR closed: GetPRStatus then reports it not open.
func (m *mockSCM) ClosePR(_ context.Context, repo string, number int) error {
	m.closed = append(m.closed, fmt.Sprintf("%s#%d", repo, number))
	if m.lostClose > 0 {
		m.lostClose--
		m.open = false
		return errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)")
	}
	if len(m.closeErrs) > 0 {
		err := m.closeErrs[0]
		m.closeErrs = m.closeErrs[1:]
		if err != nil {
			return err
		}
	}
	m.open = false
	return nil
}
func (m *mockSCM) CommentOnPR(_ context.Context, _ string, _ int, body string) error {
	m.comments = append(m.comments, body)
	return nil
}
func (m *mockSCM) GetPRStatus(_ context.Context, _ string, _ int) (bool, bool, error) {
	m.getPRCalled++
	return m.merged, m.open, m.getPRErr
}
func (m *mockSCM) GetPRReviewStatus(_ context.Context, _ string, _ int) (bool, int, error) {
	return false, 0, nil
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

func (m *mockGit) Clone(_ context.Context, _, _, _, _ string) error        { return m.cloneErr }
func (m *mockGit) CloneAt(_ context.Context, _, _, _, _ string) error      { return nil }
func (m *mockGit) CommitAll(_ context.Context, _, _, _, _ string) error    { return nil }
func (m *mockGit) Push(_ context.Context, _, _, _, _ string, _ bool) error { return nil }

// ---------- helpers ----------

func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

// healthyDeployment returns a Deployment whose rollout is complete: the
// current generation is observed, its one replica is updated and available,
// and Available=True.
func healthyDeployment(name, namespace string) *appsv1.Deployment {
	one := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &one},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Replicas:           1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
		},
	}
}

// degradedDeployment returns a Deployment whose rollout finished but whose
// replica is no longer available (Available=False): a health failure, not a
// rollout in progress.
func degradedDeployment(name, namespace string) *appsv1.Deployment {
	one := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &one},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Replicas:           1, UpdatedReplicas: 1, UnavailableReplicas: 1,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, Message: "pods not ready"},
			},
		},
	}
}

// withImage sets the Deployment's single container image.
func withImage(d *appsv1.Deployment, image string) *appsv1.Deployment {
	d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "app", Image: image}}
	return d
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

// TestWaitingForMerge_TimeoutFires verifies that a WaitForMerge timeout transitions
// the step to Failed when the expiry has elapsed.
func TestWaitingForMerge_TimeoutFires(t *testing.T) {
	scheme := buildScheme(t)

	// Build a pipeline with a 1-minute WaitForMergeTimeout on prod environment.
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test", Approval: "auto"},
				{Name: "prod", Approval: "pr-review", WaitForMergeTimeout: "1m"},
			},
		},
	}
	bundle := makeBundle("bundle-1", "nginx-demo")
	step := makeStep("step-timeout", "nginx-demo", "bundle-1", "prod")
	step.Spec.PRStatusRef = "prstatus-step-timeout"
	step.Status.State = "WaitingForMerge"
	// Pre-set an expiry in the past to simulate timeout elapsed.
	pastExpiry := metav1.NewTime(time.Now().Add(-2 * time.Minute))
	step.Status.WaitForMergeExpiry = &pastExpiry

	// PRStatus still open (not merged).
	now := metav1.Now()
	prStatus := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-step-timeout", Namespace: "default"},
		Spec:       v1alpha1.PRStatusSpec{PRURL: "https://github.com/test/repo/pull/10", PRNumber: 10, Repo: "test/repo"},
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

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-timeout", Namespace: "default"},
	})
	require.NoError(t, err)

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-timeout", Namespace: "default"}, &updated))
	assert.Equal(t, "Failed", updated.Status.State, "step should be Failed after timeout")
	assert.Contains(t, updated.Status.Message, "wait-for-merge timeout", "message should mention timeout")
	assert.Nil(t, updated.Status.WaitForMergeExpiry, "expiry should be cleared on failure")
}

// TestWaitingForMerge_TimeoutNotConfigured verifies that when no WaitForMergeTimeout is set,
// the step stays in WaitingForMerge indefinitely (no timeout).
func TestWaitingForMerge_TimeoutNotConfigured(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-no-timeout", "nginx-demo", "bundle-1", "prod")
	step.Spec.PRStatusRef = "prstatus-step-no-timeout"
	step.Status.State = "WaitingForMerge"
	// No WaitForMergeExpiry set — no timeout configured.
	pipeline := makePipeline("nginx-demo") // prod env has no WaitForMergeTimeout
	bundle := makeBundle("bundle-1", "nginx-demo")

	now := metav1.Now()
	prStatus := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-step-no-timeout", Namespace: "default"},
		Spec:       v1alpha1.PRStatusSpec{PRURL: "https://github.com/test/repo/pull/11", PRNumber: 11, Repo: "test/repo"},
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
		NamespacedName: types.NamespacedName{Name: "step-no-timeout", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0), "should requeue")

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-no-timeout", Namespace: "default"}, &updated))
	assert.Equal(t, "WaitingForMerge", updated.Status.State, "step should remain WaitingForMerge without timeout")
	assert.Nil(t, updated.Status.WaitForMergeExpiry, "no expiry should be set when timeout not configured")
}

// TestWaitingForMerge_TimeoutNotYetReached verifies that when timeout is configured
// but expiry hasn't elapsed, the step stays in WaitingForMerge.
func TestWaitingForMerge_TimeoutNotYetReached(t *testing.T) {
	scheme := buildScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test", Approval: "auto"},
				{Name: "prod", Approval: "pr-review", WaitForMergeTimeout: "24h"},
			},
		},
	}
	bundle := makeBundle("bundle-1", "nginx-demo")
	step := makeStep("step-not-expired", "nginx-demo", "bundle-1", "prod")
	step.Spec.PRStatusRef = "prstatus-step-not-expired"
	step.Status.State = "WaitingForMerge"
	// Expiry set 23 hours in the future — not yet reached.
	futureExpiry := metav1.NewTime(time.Now().Add(23 * time.Hour))
	step.Status.WaitForMergeExpiry = &futureExpiry

	now := metav1.Now()
	prStatus := &v1alpha1.PRStatus{
		ObjectMeta: metav1.ObjectMeta{Name: "prstatus-step-not-expired", Namespace: "default"},
		Spec:       v1alpha1.PRStatusSpec{PRURL: "https://github.com/test/repo/pull/12", PRNumber: 12, Repo: "test/repo"},
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
		NamespacedName: types.NamespacedName{Name: "step-not-expired", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0), "should requeue while waiting")

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-not-expired", Namespace: "default"}, &updated))
	assert.Equal(t, "WaitingForMerge", updated.Status.State, "step should remain WaitingForMerge")
	assert.NotNil(t, updated.Status.WaitForMergeExpiry, "expiry should remain set")
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

// TestAbortedByAlarmIsTerminal verifies that AbortedByAlarm is a no-op (idempotent).
// Guards against regression: AbortedByAlarm must not fall into the default switch branch
// which would reset the state to Pending (causing the Verified→Promoting cycle #789).
func TestAbortedByAlarmIsTerminal(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-aborted", "nginx-demo", "bundle-1", "test")
	step.Status.State = "AbortedByAlarm"
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
		NamespacedName: types.NamespacedName{Name: "step-aborted", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.False(t, result.Requeue, "AbortedByAlarm must not requeue") //nolint:staticcheck
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	// State must remain AbortedByAlarm — not reset to Pending or Promoting.
	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "step-aborted", Namespace: "default"}, &got))
	assert.Equal(t, "AbortedByAlarm", got.Status.State,
		"AbortedByAlarm must remain AbortedByAlarm after reconcile")
}

// TestRollingBackIsTerminal verifies that RollingBack is a no-op (idempotent).
// Guards against regression: RollingBack must not fall into the default switch branch
// which would reset the state to Pending (causing the Verified→Promoting cycle #789).
func TestRollingBackIsTerminal(t *testing.T) {
	scheme := buildScheme(t)
	step := makeStep("step-rolling", "nginx-demo", "bundle-1", "test")
	step.Status.State = "RollingBack"
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
		NamespacedName: types.NamespacedName{Name: "step-rolling", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.False(t, result.Requeue, "RollingBack must not requeue") //nolint:staticcheck
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	// State must remain RollingBack — not reset to Pending or Promoting.
	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "step-rolling", Namespace: "default"}, &got))
	assert.Equal(t, "RollingBack", got.Status.State,
		"RollingBack must remain RollingBack after reconcile")
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
	_ = steps.DefaultSequenceForBundle // ensure package import is used
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
// the PromotionStep reconciler does not read Pipeline.Spec.Paused. Pause is
// enforced by the freeze PolicyGate, which the Pipeline reconciler converges
// to spec.paused and the step holds on (TestPause_FreezeGateHoldsStep). With
// spec.paused set but no gate yet, the step still advances.
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

// TestSupersessionGuard_ClosesOpenPRAndFails verifies that when the parent Bundle
// is Superseded, a WaitingForMerge PromotionStep transitions to Failed (#310).
func TestSupersessionGuard_ClosesOpenPRAndFails(t *testing.T) {
	s := buildScheme(t)

	// Bundle is already Superseded (newer bundle took over).
	bundle := makeBundle("superseded-bundle", "my-pipeline")
	bundle.Status.Phase = "Superseded"

	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stale-prod-step",
			Namespace: "default",
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "my-pipeline",
			BundleName:   "superseded-bundle",
			Environment:  "prod",
		},
		Status: v1alpha1.PromotionStepStatus{
			State: "WaitingForMerge",
			Outputs: map[string]string{
				"prURL": "https://github.com/org/repo/pull/42",
			},
		},
	}
	pipeline := makePipeline("my-pipeline")

	mock := &mockSCM{open: true}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(step, bundle, pipeline).
		WithStatusSubresource(step).
		Build()

	r := promotionstep.Reconciler{
		Client:    c,
		SCM:       mock,
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "stale-prod-step", Namespace: "default"},
	})
	require.NoError(t, err, "supersession guard must not return an error")

	// Step must be in Failed state.
	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "stale-prod-step", Namespace: "default"}, &got))
	assert.Equal(t, "Failed", got.Status.State, "WaitingForMerge step must be Failed when bundle is Superseded")
	assert.Contains(t, got.Status.Message, "superseded", "failure message must mention supersession")
	// The open PR must be closed, with a comment saying why, or a later merge
	// would deliver a superseded version (C03-promotionstep-08).
	assert.Equal(t, []string{"org/repo#42"}, mock.closed, "the superseded step's PR must be closed")
	require.Len(t, mock.comments, 1)
	assert.Contains(t, mock.comments[0], "superseded")
}

// TestSupersessionGuard_PendingStepNeverStarts verifies that a Pending step of
// a Superseded Bundle is cancelled before it pushes or opens a PR. The
// superseded Bundle's Graph stays in place and still creates downstream steps
// once their gates open.
func TestSupersessionGuard_PendingStepNeverStarts(t *testing.T) {
	s := buildScheme(t)
	bundle := makeBundle("superseded-bundle", "my-pipeline")
	bundle.Status.Phase = "Superseded"
	step := makeStep("late-prod-step", "my-pipeline", "superseded-bundle", "prod")
	pipeline := makePipeline("my-pipeline")

	git := &mockGit{}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(step, bundle, pipeline).
		WithStatusSubresource(step).
		Build()
	r := promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: git,
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "late-prod-step", Namespace: "default"},
	})
	require.NoError(t, err)

	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "late-prod-step", Namespace: "default"}, &got))
	assert.Equal(t, "Failed", got.Status.State, "Pending step must be Failed when bundle is Superseded")
	assert.Contains(t, got.Status.Message, "superseded")
}

// --- #409: orphan cleanup lifecycle tests ---

// TestOrphanCleanup_DeleteBundle_PromotingPhase verifies that when a Bundle in
// Promoting phase is deleted (e.g. by the operator), all its PromotionSteps
// self-delete on the next reconcile (orphan guard kicks in).
// This is a regression test for issue #409 (was broken before #257/#270 fix).
func TestOrphanCleanup_DeleteBundle_PromotingPhase(t *testing.T) {
	s := buildScheme(t)

	// Bundle is in Promoting phase (active promotion in progress).
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "my-pipeline"},
		Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	step := makeStep("step-test", "my-pipeline", "nginx-demo-v1", "test")
	step.Status.State = "Promoting"
	pipeline := makePipeline("my-pipeline")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(step, pipeline, bundle).
		WithStatusSubresource(step).
		Build()

	// Delete the bundle (simulate operator action).
	require.NoError(t, c.Delete(context.Background(), bundle))

	r := promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-test", Namespace: "default"},
	})
	require.NoError(t, err, "orphan guard must not crash on deleted Promoting bundle")

	// PromotionStep must be self-deleted.
	var got v1alpha1.PromotionStep
	getErr := c.Get(context.Background(), types.NamespacedName{Name: "step-test", Namespace: "default"}, &got)
	assert.True(t, apierrors.IsNotFound(getErr) || got.DeletionTimestamp != nil,
		"PromotionStep must be deleted or marked for deletion when parent bundle is gone; got state=%s", got.Status.State)
}

// TestOrphanCleanup_DeleteBundle_FailedPhase verifies that when a Bundle in
// Failed phase is deleted, its PromotionSteps also self-delete.
func TestOrphanCleanup_DeleteBundle_FailedPhase(t *testing.T) {
	s := buildScheme(t)

	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v2", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "my-pipeline"},
		Status:     v1alpha1.BundleStatus{Phase: "Failed"},
	}
	step := makeStep("step-failed-test", "my-pipeline", "nginx-demo-v2", "test")
	step.Status.State = "Failed"
	pipeline := makePipeline("my-pipeline")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(step, pipeline, bundle).
		WithStatusSubresource(step).
		Build()

	// Delete the bundle.
	require.NoError(t, c.Delete(context.Background(), bundle))

	r := promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-failed-test", Namespace: "default"},
	})
	require.NoError(t, err, "orphan guard must not crash on deleted Failed bundle")

	var got v1alpha1.PromotionStep
	getErr := c.Get(context.Background(), types.NamespacedName{Name: "step-failed-test", Namespace: "default"}, &got)
	assert.True(t, apierrors.IsNotFound(getErr) || got.DeletionTimestamp != nil,
		"PromotionStep must be deleted or marked for deletion when parent bundle is gone")
}

// TestOrphanCleanup_MultipleStepsForBundle verifies that ALL PromotionSteps
// for a deleted bundle self-delete — not just the one being reconciled.
// Each step must reconcile itself when the bundle is gone.
func TestOrphanCleanup_MultipleStepsForBundle(t *testing.T) {
	s := buildScheme(t)

	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v3", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "my-pipeline"},
		Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	stepTest := makeStep("step3-test", "my-pipeline", "nginx-demo-v3", "test")
	stepUAT := makeStep("step3-uat", "my-pipeline", "nginx-demo-v3", "uat")
	stepProd := makeStep("step3-prod", "my-pipeline", "nginx-demo-v3", "prod")
	pipeline := makePipeline("my-pipeline")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(stepTest, stepUAT, stepProd, pipeline, bundle).
		WithStatusSubresource(stepTest, stepUAT, stepProd).
		Build()

	// Delete the bundle.
	require.NoError(t, c.Delete(context.Background(), bundle))

	r := promotionstep.Reconciler{
		Client:    c,
		SCM:       &mockSCM{},
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}

	// Reconcile each step — each one should self-delete.
	for _, stepName := range []string{"step3-test", "step3-uat", "step3-prod"} {
		_, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: stepName, Namespace: "default"},
		})
		require.NoError(t, err, "orphan guard must not crash for step %s", stepName)
	}

	// All steps must be gone.
	for _, stepName := range []string{"step3-test", "step3-uat", "step3-prod"} {
		var got v1alpha1.PromotionStep
		getErr := c.Get(context.Background(), types.NamespacedName{Name: stepName, Namespace: "default"}, &got)
		assert.True(t, apierrors.IsNotFound(getErr) || got.DeletionTimestamp != nil,
			"step %s must be deleted or marked for deletion when parent bundle is gone", stepName)
	}
}

// ─── #1300 / #1323: required gates are re-checked before the step starts ────

// gateStepCreated is the creationTimestamp of the step in the required-gate
// tests. The fake client does not set creationTimestamp, so the tests do.
var gateStepCreated = metav1.NewTime(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))

// requiredGate returns a PolicyGate with the given result, last evaluated at
// evaluatedAt (nil: never evaluated).
func requiredGate(name, when string, ready bool, evaluatedAt *metav1.Time) *v1alpha1.PolicyGate {
	return &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.PolicyGateSpec{Expression: "upstream.uat.soakMinutes >= 30", When: when}, //nolint:staticcheck // SA1019: when has no effect (#1323)
		Status:     v1alpha1.PolicyGateStatus{Ready: ready, LastEvaluatedAt: evaluatedAt},
	}
}

// messageGate is a required gate with spec.message, evaluated not ready with
// reason after the step was created.
func messageGate(name, message, reason string) *v1alpha1.PolicyGate {
	g := requiredGate(name, "", false, gateTime(time.Minute))
	g.Spec.Message = message
	g.Status.Reason = reason
	return g
}

func gateTime(d time.Duration) *metav1.Time {
	t := metav1.NewTime(gateStepCreated.Add(d))
	return &t
}

// newGatedStepReconciler returns a reconciler and client holding a Pending
// prod step, created at gateStepCreated, that requires gateNames.
func newGatedStepReconciler(t *testing.T, gateNames []string, gates ...*v1alpha1.PolicyGate) (*promotionstep.Reconciler, *mockSCM, ctrl.Request) {
	t.Helper()
	step := makeStep("step-prod", "my-app", "bundle-1", "prod")
	step.CreationTimestamp = gateStepCreated
	step.Spec.RequiredGates = gateNames
	objs := []client.Object{step, makePipeline("my-app"), makeBundle("bundle-1", "my-app")}
	for _, g := range gates {
		objs = append(objs, g)
	}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PolicyGate{}).
		Build()
	scmMock := &mockSCM{}
	r := &promotionstep.Reconciler{
		Client:    c,
		SCM:       scmMock,
		GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() },
	}
	return r, scmMock, ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-prod", Namespace: "default"}}
}

// TestCheckRequiredGates: a Pending step starts only when every required gate
// exists, is ready, and was evaluated at or after the step was created. The
// Graph created the step on a gate result that can be older than the step
// (#1300), and spec.when has no effect (#1323).
func TestCheckRequiredGates(t *testing.T) {
	tests := []struct {
		name      string
		gateNames []string
		gates     []*v1alpha1.PolicyGate
		wantStart bool
		wantMsg   string
	}{
		{
			name:      "missing gate holds",
			gateNames: []string{"soak"},
			wantMsg:   "waiting for gate soak",
		},
		{
			name:      "gate not ready holds",
			gateNames: []string{"soak"},
			gates:     []*v1alpha1.PolicyGate{requiredGate("soak", "", false, gateTime(time.Minute))},
			wantMsg:   "waiting for gate soak",
		},
		{
			// #1323: the CRD default. The gate turned false after the Graph
			// created the step, before the step started.
			name:      "post-deploy gate that turned false before start holds",
			gateNames: []string{"soak"},
			gates:     []*v1alpha1.PolicyGate{requiredGate("soak", "post-deploy", false, gateTime(time.Minute))},
			wantMsg:   "waiting for gate soak",
		},
		{
			// GATE-MESSAGE-01: the gate's message says what it waits for.
			name:      "gate blocked by its expression names its message",
			gateNames: []string{"soak"},
			gates: []*v1alpha1.PolicyGate{messageGate("soak", "uat must soak for 30 minutes",
				"uat must soak for 30 minutes (bundle.version=1.2.0: upstream.uat.soakMinutes >= 30 = false)")},
			wantMsg: "waiting for gate soak: uat must soak for 30 minutes",
		},
		{
			// The message does not explain an evaluation error.
			name:      "gate blocked by an evaluation error keeps the plain message",
			gateNames: []string{"soak"},
			gates: []*v1alpha1.PolicyGate{messageGate("soak", "uat must soak for 30 minutes",
				"CEL evaluation error: no such key: uat")},
			wantMsg: "waiting for gate soak",
		},
		{
			name:      "ready gate never evaluated holds",
			gateNames: []string{"soak"},
			gates:     []*v1alpha1.PolicyGate{requiredGate("soak", "", true, nil)},
			wantMsg:   "waiting for gate soak to be re-evaluated",
		},
		{
			// #1300: the result the Graph acted on is older than the step.
			name:      "step newer than the gate's lastEvaluatedAt holds",
			gateNames: []string{"soak"},
			gates:     []*v1alpha1.PolicyGate{requiredGate("soak", "post-deploy", true, gateTime(-time.Minute))},
			wantMsg:   "waiting for gate soak to be re-evaluated",
		},
		{
			// metav1.Time has one-second precision: same second counts.
			name:      "gate evaluated in the second the step was created starts",
			gateNames: []string{"soak"},
			gates:     []*v1alpha1.PolicyGate{requiredGate("soak", "", true, gateTime(0))},
			wantStart: true,
		},
		{
			name:      "gate evaluated after the step was created starts",
			gateNames: []string{"soak"},
			gates:     []*v1alpha1.PolicyGate{requiredGate("soak", "post-deploy", true, gateTime(time.Minute))},
			wantStart: true,
		},
		{
			name:      "pre-deploy gate evaluated after the step was created starts",
			gateNames: []string{"soak"},
			gates:     []*v1alpha1.PolicyGate{requiredGate("soak", "pre-deploy", true, gateTime(time.Minute))},
			wantStart: true,
		},
		{
			name:      "one of two gates not re-evaluated holds",
			gateNames: []string{"soak", "hours"},
			gates: []*v1alpha1.PolicyGate{
				requiredGate("soak", "", true, gateTime(time.Minute)),
				requiredGate("hours", "", true, gateTime(-time.Second)),
			},
			wantMsg: "waiting for gate hours to be re-evaluated",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, scmMock, req := newGatedStepReconciler(t, tt.gateNames, tt.gates...)
			result, err := r.Reconcile(context.Background(), req)
			require.NoError(t, err)

			var got v1alpha1.PromotionStep
			require.NoError(t, r.Get(context.Background(), req.NamespacedName, &got))
			if tt.wantStart {
				assert.Equal(t, "Promoting", got.Status.State)
				return
			}
			assert.Empty(t, got.Status.State, "a held step stays Pending")
			assert.Equal(t, tt.wantMsg, got.Status.Message)
			assert.Empty(t, got.Status.Steps, "no step sequence before the gates pass")
			assert.Zero(t, scmMock.openCalled, "no PR while a gate holds")
			assert.Greater(t, result.RequeueAfter, time.Duration(0), "a held step requeues")
		})
	}
}

// TestCheckRequiredGates_PassesAfterReEvaluation: a step held on a gate
// result older than the step starts once the gate is re-evaluated.
func TestCheckRequiredGates_PassesAfterReEvaluation(t *testing.T) {
	r, _, req := newGatedStepReconciler(t, []string{"soak"},
		requiredGate("soak", "", true, gateTime(-time.Minute)))
	ctx := context.Background()

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	var got v1alpha1.PromotionStep
	require.NoError(t, r.Get(ctx, req.NamespacedName, &got))
	require.Empty(t, got.Status.State, "held on the stale gate result")
	require.Equal(t, "waiting for gate soak to be re-evaluated", got.Status.Message)

	// The PolicyGate reconciler re-evaluates the gate after the step exists.
	var gate v1alpha1.PolicyGate
	require.NoError(t, r.Get(ctx, types.NamespacedName{Name: "soak", Namespace: "default"}, &gate))
	gate.Status.LastEvaluatedAt = gateTime(5 * time.Second)
	require.NoError(t, r.Status().Update(ctx, &gate))

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, r.Get(ctx, req.NamespacedName, &got))
	assert.Equal(t, "Promoting", got.Status.State)
}

// ─── K-01: Contiguous soak / bake ────────────────────────────────────────────

// TestBakeTracking_FirstHealthyPass verifies that when a PromotionStep enters
// HealthChecking with a bake config, the first healthy check sets BakeStartedAt
// and begins accumulating BakeElapsedMinutes.
func TestBakeTracking_FirstHealthyPass(t *testing.T) {
	s := buildScheme(t)
	pipeline := minimalPipelineWithBake(t, "my-app", "prod", 30) // bake: 30 minutes

	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-prod", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "my-app",
			BundleName:   "my-app-v1",
			Environment:  "prod",
			StepType:     "health-check",
		},
		Status: v1alpha1.PromotionStepStatus{
			State: "HealthChecking",
		},
	}

	// Healthy deployment for the resource adapter
	deploy := healthyDeployment("my-app", "prod")
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, ps, deploy, bundle).
		WithStatusSubresource(&v1alpha1.PromotionStep{}).
		Build()

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	healthDet := health.NewAutoDetector(c, dynClient)
	r := &promotionstep.Reconciler{
		Client:         c,
		SCM:            &mockSCM{},
		GitClient:      &mockGit{},
		HealthDetector: healthDet,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-app-prod", Namespace: "default"}}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))

	// With bake configured and health=healthy, BakeStartedAt must be set
	assert.NotNil(t, got.Status.BakeStartedAt,
		"BakeStartedAt must be set when health passes with bake config")
	// BakeElapsedMinutes starts at 0 on first pass (no elapsed time yet)
	assert.GreaterOrEqual(t, got.Status.BakeElapsedMinutes, int64(0))
	// Step should NOT be Verified yet — needs to accumulate bake minutes
	assert.NotEqual(t, "Verified", got.Status.State,
		"step must not be Verified before bake duration completes")
}

// TestBakeTracking_HealthFailureResetsTimer verifies that a health failure
// during the bake window resets BakeElapsedMinutes to 0 when policy=reset-on-alarm.
func TestBakeTracking_HealthFailureResetsTimer(t *testing.T) {
	s := buildScheme(t)
	pipeline := minimalPipelineWithBake(t, "my-app", "prod", 30)

	startedAt := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-prod", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "my-app",
			BundleName:   "my-app-v1",
			Environment:  "prod",
			StepType:     "health-check",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:              "HealthChecking",
			BakeStartedAt:      &startedAt,
			BakeElapsedMinutes: 8, // had 8 minutes accumulated
		},
	}

	// Unhealthy deployment — health alarm fires
	deploy := degradedDeployment("my-app", "prod")
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, ps, deploy, bundle).
		WithStatusSubresource(&v1alpha1.PromotionStep{}).
		Build()

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	healthDet := health.NewAutoDetector(c, dynClient)
	r := &promotionstep.Reconciler{
		Client:         c,
		SCM:            &mockSCM{},
		GitClient:      &mockGit{},
		HealthDetector: healthDet,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-app-prod", Namespace: "default"}}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))

	// reset-on-alarm: timer resets to 0
	assert.Equal(t, int64(0), got.Status.BakeElapsedMinutes,
		"BakeElapsedMinutes must reset to 0 on health failure (reset-on-alarm policy)")
	assert.Greater(t, got.Status.BakeResets, 0,
		"BakeResets counter must increment on health alarm")
	// Step stays in HealthChecking — not Failed, not Verified
	assert.Equal(t, "HealthChecking", got.Status.State,
		"step must remain HealthChecking after bake reset")
}

// TestBakeTracking_VerifiedAfterFullBake verifies that the step transitions to
// Verified when BakeElapsedMinutes >= bake.minutes.
func TestBakeTracking_VerifiedAfterFullBake(t *testing.T) {
	s := buildScheme(t)
	pipeline := minimalPipelineWithBake(t, "my-app", "prod", 30) // 30 min bake

	startedAt := metav1.NewTime(time.Now().Add(-31 * time.Minute))
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-prod", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "my-app",
			BundleName:   "my-app-v1",
			Environment:  "prod",
			StepType:     "health-check",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:              "HealthChecking",
			BakeStartedAt:      &startedAt,
			BakeElapsedMinutes: 30, // exactly at threshold
		},
	}

	deploy := healthyDeployment("my-app", "prod")
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-v1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "my-app"},
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, ps, deploy, bundle).
		WithStatusSubresource(&v1alpha1.PromotionStep{}).
		Build()

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	healthDet := health.NewAutoDetector(c, dynClient)
	r := &promotionstep.Reconciler{
		Client:         c,
		SCM:            &mockSCM{},
		GitClient:      &mockGit{},
		HealthDetector: healthDet,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-app-prod", Namespace: "default"}}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))

	assert.Equal(t, "Verified", got.Status.State,
		"step must be Verified once BakeElapsedMinutes >= bake.minutes")
}

// minimalPipelineWithBake creates a Pipeline with bake config for testing.
func minimalPipelineWithBake(t *testing.T, name, env string, bakeMinutes int) *v1alpha1.Pipeline {
	t.Helper()
	return &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{
					Name:     env,
					Approval: "auto",
					Health:   v1alpha1.HealthConfig{Type: "resource"},
					Bake: &v1alpha1.BakeConfig{
						Minutes: bakeMinutes,
						Policy:  "reset-on-alarm",
					},
				},
			},
		},
	}
}

// ─── K-03: Abort vs Rollback on health failure ────────────────────────────────

// TestOnHealthFailure_AbortSetsAbortedState verifies that onHealthFailure=abort
// transitions the step to AbortedByAlarm when health fails (fail-on-alarm policy).
func TestOnHealthFailure_AbortSetsAbortedState(t *testing.T) {
	scheme := buildScheme(t)
	pipeline := pipelineWithHealthFailurePolicy(t, "my-app", "prod", "abort")

	startedAt := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app-prod", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "my-app",
			BundleName:   "my-app-v1",
			Environment:  "prod",
			StepType:     "health-check",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:         "HealthChecking",
			BakeStartedAt: &startedAt,
		},
	}

	// Unhealthy deployment
	deploy := degradedDeployment("my-app", "prod")
	bundle := makeBundle("my-app-v1", "my-app")
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(pipeline, ps, deploy, bundle).
		WithStatusSubresource(&v1alpha1.PromotionStep{}).
		Build()

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	healthDet := health.NewAutoDetector(c, dynClient)
	r := &promotionstep.Reconciler{
		Client:         c,
		SCM:            &mockSCM{},
		GitClient:      &mockGit{},
		HealthDetector: healthDet,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-app-prod", Namespace: "default"}}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))

	assert.Equal(t, "AbortedByAlarm", got.Status.State,
		"step must be AbortedByAlarm when onHealthFailure=abort and health fails")
}

// pipelineWithHealthFailurePolicy creates a Pipeline with bake + onHealthFailure for K-03 tests.
func pipelineWithHealthFailurePolicy(t *testing.T, name, env, policy string) *v1alpha1.Pipeline {
	t.Helper()
	return &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{
					Name:            env,
					Approval:        "auto",
					Health:          v1alpha1.HealthConfig{Type: "resource"},
					OnHealthFailure: policy,
					Bake: &v1alpha1.BakeConfig{
						Minutes: 30,
						Policy:  "fail-on-alarm", // triggers onHealthFailure on health failure
					},
				},
			},
		},
	}
}
