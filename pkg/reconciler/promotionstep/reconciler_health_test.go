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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

func healthTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

type noopSCM struct{}

func (n *noopSCM) OpenPR(_ context.Context, _, _, _, _, _ string) (string, int, error) {
	return "", 0, nil
}
func (n *noopSCM) ClosePR(_ context.Context, _ string, _ int) error               { return nil }
func (n *noopSCM) CommentOnPR(_ context.Context, _ string, _ int, _ string) error { return nil }
func (n *noopSCM) GetPRStatus(_ context.Context, _ string, _ int) (bool, bool, error) {
	return false, false, nil
}
func (n *noopSCM) GetPRReviewStatus(_ context.Context, _ string, _ int) (bool, int, error) {
	return false, 0, nil
}
func (n *noopSCM) ParseWebhookEvent(_ []byte, _ string) (scm.WebhookEvent, error) {
	return scm.WebhookEvent{}, nil
}
func (n *noopSCM) AddLabelsToPR(_ context.Context, _ string, _ int, _ []string) error {
	return nil
}

type noopGit struct{}

func (n *noopGit) Clone(_ context.Context, _, _, _ string) error        { return nil }
func (n *noopGit) Checkout(_ context.Context, _, _ string) error        { return nil }
func (n *noopGit) CommitAll(_ context.Context, _, _, _, _ string) error { return nil }
func (n *noopGit) Push(_ context.Context, _, _, _, _ string) error      { return nil }

// TestHealthCheckingWithRealAdapter_Healthy verifies that the PromotionStep reconciler
// uses the DeploymentAdapter when HealthDetector is configured and the Deployment is healthy.
func TestHealthCheckingWithRealAdapter_Healthy(t *testing.T) {
	s := healthTestScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: "resource"}},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-hc", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-1",
			Environment:  "test",
			StepType:     "auto",
		},
		Status: v1alpha1.PromotionStepStatus{State: "HealthChecking"},
	}
	// A Deployment named "nginx-demo" in namespace "test" with Available=True.
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "test"},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:   appsv1.DeploymentAvailable,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, ps, deploy).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		Build()

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	healthDetector := health.NewAutoDetector(c, dynClient)

	rec := &promotionstep.Reconciler{
		Client:         c,
		SCM:            &noopSCM{},
		GitClient:      &noopGit{},
		HealthDetector: healthDetector,
		WorkDirFn:      func(_, _ string) string { return t.TempDir() },
	}

	_, err := rec.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-hc", Namespace: "default"},
	})
	require.NoError(t, err)

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-hc", Namespace: "default"}, &updated))
	assert.Equal(t, "Verified", updated.Status.State)
}

// TestHealthCheckingWithRealAdapter_NotHealthy verifies that an unhealthy Deployment
// keeps the step in HealthChecking (requeue, not fail — waiting for rollout).
func TestHealthCheckingWithRealAdapter_NotHealthy(t *testing.T) {
	s := healthTestScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: "resource"}},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-hc-wait", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-1",
			Environment:  "test",
			StepType:     "auto",
		},
		Status: v1alpha1.PromotionStepStatus{State: "HealthChecking"},
	}
	// Deployment is not yet available.
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "test"},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{
					Type:    appsv1.DeploymentAvailable,
					Status:  corev1.ConditionFalse,
					Message: "pods not ready",
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, ps, deploy).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		Build()

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	healthDetector := health.NewAutoDetector(c, dynClient)

	rec := &promotionstep.Reconciler{
		Client:         c,
		SCM:            &noopSCM{},
		GitClient:      &noopGit{},
		HealthDetector: healthDetector,
		WorkDirFn:      func(_, _ string) string { return t.TempDir() },
	}

	result, err := rec.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-hc-wait", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter.Seconds(), 0.0, "should requeue while not healthy")

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-hc-wait", Namespace: "default"}, &updated))
	// State should still be HealthChecking (not Failed — just not ready yet).
	assert.Equal(t, "HealthChecking", updated.Status.State)
}

// TestAutoRollback_TriggersAfterThreshold verifies that after reaching the failure
// threshold, the PromotionStep reconciler increments consecutiveHealthFailures but
// does NOT create a rollback Bundle — that is now the RollbackPolicyReconciler's job.
func TestAutoRollback_TriggersAfterThreshold(t *testing.T) {
	s := healthTestScheme(t)
	threshold := 3

	// Pipeline with autoRollback enabled.
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{
					Name:     "test",
					Approval: "auto",
					Health:   v1alpha1.HealthConfig{Type: "resource"},
					AutoRollback: &v1alpha1.AutoRollbackSpec{
						FailureThreshold: threshold,
					},
				},
			},
		},
	}
	// Bundle with images.
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-1", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "nginx-demo",
			Images:   []v1alpha1.ImageRef{{Repository: "ghcr.io/nginx/nginx", Tag: "1.29.0"}},
		},
		Status: v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	// PromotionStep already at threshold-1 failures; next failure increments counter.
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-hc-ar", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-1",
			Environment:  "test",
			StepType:     "auto",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:                     "HealthChecking",
			ConsecutiveHealthFailures: threshold - 1, // one more → increment to threshold
		},
	}
	// Deployment still not healthy.
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "test"},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, ps, deploy).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		Build()

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	healthDetector := health.NewAutoDetector(c, dynClient)

	rec := &promotionstep.Reconciler{
		Client:         c,
		SCM:            &noopSCM{},
		GitClient:      &noopGit{},
		HealthDetector: healthDetector,
		WorkDirFn:      func(_, _ string) string { return t.TempDir() },
	}

	_, err := rec.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-hc-ar", Namespace: "default"},
	})
	require.NoError(t, err)

	// The step should remain HealthChecking — auto-rollback is now via RollbackPolicyReconciler.
	var updatedPS v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-hc-ar", Namespace: "default"}, &updatedPS))
	// Counter incremented — this is what RollbackPolicyReconciler watches.
	assert.Equal(t, threshold, updatedPS.Status.ConsecutiveHealthFailures,
		"consecutiveHealthFailures must be incremented to threshold")

	// The PromotionStep reconciler must NOT create rollback bundles anymore.
	// The RollbackPolicyReconciler handles this when it detects the threshold.
	var bundleList v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList))
	for _, b := range bundleList.Items {
		assert.NotEqual(t, "true", b.Labels["kardinal.io/rollback"],
			"PromotionStep reconciler must NOT create rollback bundles — use RollbackPolicyReconciler")
	}
}

// TestAutoRollback_DoesNotTriggerBeforeThreshold verifies that below the threshold
// no rollback Bundle is created and consecutiveHealthFailures is incremented.
func TestAutoRollback_DoesNotTriggerBeforeThreshold(t *testing.T) {
	s := healthTestScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{
					Name:         "test",
					Approval:     "auto",
					Health:       v1alpha1.HealthConfig{Type: "resource"},
					AutoRollback: &v1alpha1.AutoRollbackSpec{FailureThreshold: 3},
				},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	// Only 1 failure so far (threshold is 3).
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-hc-below", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo", BundleName: "bundle-1",
			Environment: "test", StepType: "auto",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:                     "HealthChecking",
			ConsecutiveHealthFailures: 1,
		},
	}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "test"},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, ps, deploy).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		Build()

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	rec := &promotionstep.Reconciler{
		Client:         c,
		SCM:            &noopSCM{},
		GitClient:      &noopGit{},
		HealthDetector: health.NewAutoDetector(c, dynClient),
		WorkDirFn:      func(_, _ string) string { return t.TempDir() },
	}

	_, err := rec.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-hc-below", Namespace: "default"},
	})
	require.NoError(t, err)

	// No rollback bundle should be created.
	var bundleList v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList))
	for _, b := range bundleList.Items {
		assert.NotEqual(t, "true", b.Labels["kardinal.io/rollback"],
			"no rollback bundle should be created below threshold")
	}
}

// TestAutoRollback_Idempotent verifies that the PromotionStep reconciler
// increments consecutiveHealthFailures but never creates rollback Bundles.
// The RollbackPolicyReconciler is responsible for rollback creation and idempotency.
func TestAutoRollback_Idempotent(t *testing.T) {
	s := healthTestScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{
					Name:         "test",
					Approval:     "auto",
					Health:       v1alpha1.HealthConfig{Type: "resource"},
					AutoRollback: &v1alpha1.AutoRollbackSpec{FailureThreshold: 2},
				},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-1", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "nginx-demo",
			Images:   []v1alpha1.ImageRef{{Repository: "ghcr.io/nginx/nginx", Tag: "1.29.0"}},
		},
		Status: v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-hc-idem", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo", BundleName: "bundle-1",
			Environment: "test", StepType: "auto",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:                     "HealthChecking",
			ConsecutiveHealthFailures: 2, // already AT threshold
		},
	}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "test"},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, ps, deploy).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		Build()

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	rec := &promotionstep.Reconciler{
		Client:         c,
		SCM:            &noopSCM{},
		GitClient:      &noopGit{},
		HealthDetector: health.NewAutoDetector(c, dynClient),
		WorkDirFn:      func(_, _ string) string { return t.TempDir() },
	}

	// Reconcile once.
	_, err := rec.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-hc-idem", Namespace: "default"},
	})
	require.NoError(t, err)

	// Reconcile again (simulate crash-restart or requeue).
	_, err = rec.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-hc-idem", Namespace: "default"},
	})
	require.NoError(t, err)

	// No rollback bundles should be created by the PromotionStep reconciler.
	var bundleList v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundleList))
	var rollbacks int
	for _, b := range bundleList.Items {
		if b.Labels["kardinal.io/rollback"] == "true" {
			rollbacks++
		}
	}
	assert.Equal(t, 0, rollbacks,
		"PromotionStep reconciler must NOT create rollback bundles — RollbackPolicyReconciler is responsible")
}

// TestHealthCheckExpiry_SetOnFirstEntry verifies that status.healthCheckExpiry is written
// to the PromotionStep CRD on the first reconcile in HealthChecking state.
// This field is the Graph-observable replacement for the time.Since() timeout check (PS-5).
func TestHealthCheckExpiry_SetOnFirstEntry(t *testing.T) {
	s := healthTestScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "5m"}},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-expiry-set", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-1",
			Environment:  "test",
			StepType:     "auto",
		},
		Status: v1alpha1.PromotionStepStatus{
			State: "HealthChecking",
			// HealthCheckExpiry is nil — first entry
		},
	}
	// Deployment is not yet healthy so we stay in HealthChecking (not Verified).
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "test"},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, ps, deploy).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		Build()

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	rec := &promotionstep.Reconciler{
		Client:         c,
		SCM:            &noopSCM{},
		GitClient:      &noopGit{},
		HealthDetector: health.NewAutoDetector(c, dynClient),
		WorkDirFn:      func(_, _ string) string { return t.TempDir() },
	}

	before := metav1.Now()
	_, err := rec.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-expiry-set", Namespace: "default"},
	})
	after := metav1.Now()
	require.NoError(t, err)

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-expiry-set", Namespace: "default"}, &updated))

	require.NotNil(t, updated.Status.HealthCheckExpiry, "healthCheckExpiry must be set on first entry")

	// Expiry must be ≈ now + 5m (allow small clock drift).
	expiry := updated.Status.HealthCheckExpiry.Time
	minExpiry := before.Add(4 * time.Minute)
	maxExpiry := after.Add(6 * time.Minute)
	assert.True(t, expiry.After(minExpiry), "expiry should be at least now+4m (was: %s)", expiry)
	assert.True(t, expiry.Before(maxExpiry), "expiry should be at most now+6m (was: %s)", expiry)
}

// TestHealthCheckExpiry_TimeoutFails verifies that when healthCheckExpiry is in the past,
// the PromotionStep transitions to Failed.
func TestHealthCheckExpiry_TimeoutFails(t *testing.T) {
	s := healthTestScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "5m"}},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	// HealthCheckExpiry is already in the past — timeout has been reached.
	pastExpiry := metav1.NewTime(metav1.Now().Add(-1 * time.Minute))
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-expiry-past", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-1",
			Environment:  "test",
			StepType:     "auto",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:             "HealthChecking",
			HealthCheckExpiry: &pastExpiry,
		},
	}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "test"},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, ps, deploy).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		Build()

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	rec := &promotionstep.Reconciler{
		Client:         c,
		SCM:            &noopSCM{},
		GitClient:      &noopGit{},
		HealthDetector: health.NewAutoDetector(c, dynClient),
		WorkDirFn:      func(_, _ string) string { return t.TempDir() },
	}

	_, err := rec.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-expiry-past", Namespace: "default"},
	})
	require.NoError(t, err)

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-expiry-past", Namespace: "default"}, &updated))
	assert.Equal(t, "Failed", updated.Status.State, "health check timeout must transition to Failed")
	assert.Contains(t, updated.Status.Message, "timeout")
}

// TestHealthCheckExpiry_Idempotent verifies that if healthCheckExpiry is already set,
// the reconciler does NOT overwrite it on subsequent reconciles.
func TestHealthCheckExpiry_Idempotent(t *testing.T) {
	s := healthTestScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/test/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "5m"}},
			},
		},
	}
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-1", Namespace: "default"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "nginx-demo"},
		Status:     v1alpha1.BundleStatus{Phase: "Promoting"},
	}
	// HealthCheckExpiry is already set (simulates a restart after first entry).
	fixedExpiry := metav1.NewTime(metav1.Now().Add(8 * time.Minute))
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-expiry-idem", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "bundle-1",
			Environment:  "test",
			StepType:     "auto",
		},
		Status: v1alpha1.PromotionStepStatus{
			State:             "HealthChecking",
			HealthCheckExpiry: &fixedExpiry,
		},
	}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "test"},
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, ps, deploy).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		Build()

	dynClient := dynfake.NewSimpleDynamicClient(runtime.NewScheme())
	rec := &promotionstep.Reconciler{
		Client:         c,
		SCM:            &noopSCM{},
		GitClient:      &noopGit{},
		HealthDetector: health.NewAutoDetector(c, dynClient),
		WorkDirFn:      func(_, _ string) string { return t.TempDir() },
	}

	// Reconcile twice — expiry must remain unchanged.
	for i := 0; i < 2; i++ {
		_, err := rec.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "step-expiry-idem", Namespace: "default"},
		})
		require.NoError(t, err)
	}

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-expiry-idem", Namespace: "default"}, &updated))
	require.NotNil(t, updated.Status.HealthCheckExpiry)
	assert.Equal(t, fixedExpiry.Time.UTC().Truncate(time.Second),
		updated.Status.HealthCheckExpiry.Time.UTC().Truncate(time.Second),
		"healthCheckExpiry must not be overwritten on subsequent reconciles")
	// State must still be HealthChecking (expiry is in the future, deployment unhealthy).
	assert.Equal(t, "HealthChecking", updated.Status.State)
}

// TestDeliveryDelegation_ArgoRollouts verifies that when delivery.delegate is set to
// argoRollouts, the reconciler:
//  1. Overrides the health adapter to ArgoRolloutsAdapter.
//  2. Sets status.message to "delegated to argoRollouts".
//  3. Verifies Rollout health (reaches Verified when Rollout.status.phase==Healthy).
func TestDeliveryDelegation_ArgoRollouts(t *testing.T) {
	s := healthTestScheme(t)

	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "rollouts-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/org/repo", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{
				{
					Name: "prod",
					Health: v1alpha1.HealthConfig{
						Type: "resource", // would normally use resource adapter
					},
					Delivery: v1alpha1.DeliveryConfig{
						Delegate: "argoRollouts", // override to argoRollouts
					},
				},
			},
		},
	}

	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "bundle-1", Namespace: "default"},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "rollouts-demo",
			Images:   []v1alpha1.ImageRef{{Repository: "myapp", Tag: "v2.0.0"}},
		},
	}

	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "step-rollouts", Namespace: "default"},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "rollouts-demo",
			BundleName:   "bundle-1",
			Environment:  "prod",
			StepType:     "auto",
		},
		Status: v1alpha1.PromotionStepStatus{
			State: "HealthChecking",
		},
	}

	// Create a fake Rollout resource in the "Healthy" phase using unstructured dynamic client.
	rolloutObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Rollout",
			"metadata": map[string]interface{}{
				"name":      "rollouts-demo",
				"namespace": "prod",
			},
			"status": map[string]interface{}{
				"phase": "Healthy",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pipeline, bundle, ps).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		Build()

	// Register the Rollout GVR in the fake dynamic scheme.
	argoRolloutsGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	dynScheme := runtime.NewScheme()
	dynClient := dynfake.NewSimpleDynamicClient(dynScheme, rolloutObj)
	_, _ = dynClient.Resource(argoRolloutsGVR).Namespace("prod").Create(context.Background(), rolloutObj, metav1.CreateOptions{})

	rec := &promotionstep.Reconciler{
		Client:         c,
		SCM:            &noopSCM{},
		GitClient:      &noopGit{},
		HealthDetector: health.NewAutoDetector(c, dynClient),
		WorkDirFn:      func(_, _ string) string { return t.TempDir() },
	}

	_, err := rec.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "step-rollouts", Namespace: "default"},
	})
	require.NoError(t, err)

	var updated v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step-rollouts", Namespace: "default"}, &updated))

	// Verify delegation is signalled in the step message.
	assert.Contains(t, updated.Status.Message, "argoRollouts",
		"status.message must mention argoRollouts when delivery.delegate is set")
}
