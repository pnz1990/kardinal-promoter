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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
