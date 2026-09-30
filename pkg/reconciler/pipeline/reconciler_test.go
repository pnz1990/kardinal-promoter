// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = kardinalv1alpha1.AddToScheme(s)
	return s
}

func newPipeline(name string, envs []kardinalv1alpha1.EnvironmentSpec) *kardinalv1alpha1.Pipeline {
	return &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{
				URL: "https://github.com/myorg/gitops.git",
			},
			Environments: envs,
		},
	}
}

// TestPipelineReconciler_SetsValidCondition verifies that a valid Pipeline
// gets a Ready=True/Valid condition after reconciliation (C02-bundle-15, E2E-05).
func TestPipelineReconciler_SetsValidCondition(t *testing.T) {
	p := newPipeline("nginx-demo", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "uat", DependsOn: []string{"test"}},
		{Name: "prod", DependsOn: []string{"uat"}},
	})

	c := newClientWithIndex(newScheme(), p)

	r := &pipeline.Reconciler{Client: c}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo", Namespace: "default"},
	})

	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "nginx-demo", Namespace: "default",
	}, &got))

	require.Len(t, got.Status.Conditions, 1)
	cond := got.Status.Conditions[0]
	assert.Equal(t, "Ready", cond.Type)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "Valid", cond.Reason)
}

// TestPipelineReconciler_DuplicateEnvironmentNames verifies that a Pipeline with
// duplicate environment names gets a ValidationFailed condition.
func TestPipelineReconciler_DuplicateEnvironmentNames(t *testing.T) {
	p := newPipeline("bad-pipeline", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "test"}, // duplicate
	})

	c := newClientWithIndex(newScheme(), p)

	r := &pipeline.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "bad-pipeline", Namespace: "default"},
	})
	require.NoError(t, err) // reconciler returns no error — error is in status

	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "bad-pipeline", Namespace: "default",
	}, &got))

	require.Len(t, got.Status.Conditions, 1)
	cond := got.Status.Conditions[0]
	assert.Equal(t, "Ready", cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "ValidationFailed", cond.Reason)
	assert.Contains(t, cond.Message, "duplicate environment name")
}

// TestPipelineReconciler_DependsOnNonExistentEnv verifies that a Pipeline where
// dependsOn references an unknown environment gets a ValidationFailed condition.
func TestPipelineReconciler_DependsOnNonExistentEnv(t *testing.T) {
	p := newPipeline("bad-deps", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", DependsOn: []string{"staging"}}, // "staging" doesn't exist
	})

	c := newClientWithIndex(newScheme(), p)

	r := &pipeline.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "bad-deps", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "bad-deps", Namespace: "default",
	}, &got))

	require.Len(t, got.Status.Conditions, 1)
	cond := got.Status.Conditions[0]
	assert.Equal(t, "Ready", cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "ValidationFailed", cond.Reason)
	assert.Contains(t, cond.Message, "staging")
}

// E2E-R14: a Pipeline that sets a reserved, unimplemented field gets
// Ready=False/NotImplemented, the same answer as "kardinal validate", instead
// of Ready=True/Valid while its Bundles fail.
func TestPipelineReconciler_UnimplementedFieldsNotReady(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(p *kardinalv1alpha1.Pipeline)
		wantMsg string
	}{
		{name: "steps", wantMsg: "spec.environments[].steps is not implemented",
			mutate: func(p *kardinalv1alpha1.Pipeline) {
				p.Spec.Environments[0].Steps = []kardinalv1alpha1.StepSpec{{Uses: "git-clone"}}
			}},
		{name: "promotionTemplate", wantMsg: "spec.environments[].promotionTemplate is not implemented",
			mutate: func(p *kardinalv1alpha1.Pipeline) {
				p.Spec.Environments[0].PromotionTemplate = &kardinalv1alpha1.PromotionTemplateRef{Name: "t"}
			}},
		{name: "two regions", wantMsg: `environment "test": regions is not supported; declare one environment per region`,
			mutate: func(p *kardinalv1alpha1.Pipeline) {
				p.Spec.Environments[0].Regions = []string{"us-east-1", "eu-west-1"}
			}},
		// #1321: distributed mode was removed, so a shard is rejected.
		{name: "shard", wantMsg: `environment "test": shard is not supported: distributed mode was removed`,
			mutate: func(p *kardinalv1alpha1.Pipeline) { p.Spec.Environments[0].Shard = "eu" }},
		{name: "pipeline layout branch", wantMsg: "spec.git.layout: branch is not implemented",
			mutate: func(p *kardinalv1alpha1.Pipeline) { p.Spec.Git.Layout = "branch" }},
		{name: "environment layout branch", wantMsg: `environment "test": layout: branch is not implemented`,
			mutate: func(p *kardinalv1alpha1.Pipeline) { p.Spec.Environments[0].Layout = "branch" }},
		{name: "autoRollback", wantMsg: "autoRollback is not implemented",
			mutate: func(p *kardinalv1alpha1.Pipeline) {
				p.Spec.Environments[0].AutoRollback = &kardinalv1alpha1.AutoRollbackSpec{}
			}},
		{name: "health.cluster", wantMsg: `environment "test": health.cluster is not supported`,
			mutate: func(p *kardinalv1alpha1.Pipeline) { p.Spec.Environments[0].Health.Cluster = "prod-eu" }},
		{name: "health.resource.kind", wantMsg: `environment "test": health.resource.kind "StatefulSet" is not supported`,
			mutate: func(p *kardinalv1alpha1.Pipeline) {
				p.Spec.Environments[0].Health.Resource = &kardinalv1alpha1.ResourceRef{Kind: "StatefulSet"}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newPipeline("reserved", []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}})
			tc.mutate(p)
			c := newClientWithIndex(newScheme(), p)
			r := &pipeline.Reconciler{Client: c}
			_, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "reserved", Namespace: "default"},
			})
			require.NoError(t, err)

			var got kardinalv1alpha1.Pipeline
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "reserved", Namespace: "default"}, &got))
			cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, "NotImplemented", cond.Reason)
			assert.Contains(t, cond.Message, tc.wantMsg)
			// Most fields fail a Bundle only in the environment that sets them.
			assert.NotContains(t, cond.Message, "every Bundle")
			assert.True(t, strings.HasPrefix(cond.Message, "not implemented or not supported, so a Bundle fails "+
				"when it reaches an environment that uses one (steps, promotionTemplate and two or more regions "+
				"fail it when its Graph is built): "),
				cond.Message)
		})
	}
}

// TestPipelineReconciler_SecretRefNamespaceInvalid: a git.secretRef in another
// namespace is refused on purpose (the controller would push another
// namespace's token to a URL the Pipeline author controls), so the Pipeline is
// Ready=False/ValidationFailed, not NotImplemented.
func TestPipelineReconciler_SecretRefNamespaceInvalid(t *testing.T) {
	tests := []struct {
		name       string
		secretNS   string
		wantStatus metav1.ConditionStatus
		wantReason string
		wantMsg    string
	}{
		{name: "another namespace", secretNS: "kardinal-system", wantStatus: metav1.ConditionFalse,
			wantReason: "ValidationFailed",
			wantMsg:    `git.secretRef.namespace "kardinal-system" is not allowed: the Secret must be in the Pipeline's namespace "default"`},
		{name: "the Pipeline's namespace", secretNS: "default", wantStatus: metav1.ConditionTrue, wantReason: "Valid"},
		{name: "no namespace", wantStatus: metav1.ConditionTrue, wantReason: "Valid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newPipeline("app", []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}})
			p.Spec.Git.SecretRef = &kardinalv1alpha1.SecretRef{Name: "github-token", Namespace: tc.secretNS}
			c := newClientWithIndex(newScheme(), p)
			key := types.NamespacedName{Name: "app", Namespace: "default"}
			_, err := (&pipeline.Reconciler{Client: c}).Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
			require.NoError(t, err)

			var got kardinalv1alpha1.Pipeline
			require.NoError(t, c.Get(context.Background(), key, &got))
			cond := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			require.NotNil(t, cond)
			assert.Equal(t, tc.wantStatus, cond.Status)
			assert.Equal(t, tc.wantReason, cond.Reason)
			assert.Contains(t, cond.Message, tc.wantMsg)
		})
	}
}

// TestPipelineReconciler_Idempotent verifies that if a Pipeline already has the
// correct Valid condition, reconcile is a no-op and keeps lastTransitionTime.
func TestPipelineReconciler_Idempotent(t *testing.T) {
	p := newPipeline("nginx-demo", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "prod", DependsOn: []string{"test"}},
	})
	// Pre-populate the condition as if a previous reconcile already ran
	ltt := metav1.NewTime(metav1.Now().Add(-time.Hour).Truncate(time.Second))
	p.Status.Conditions = []metav1.Condition{
		{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "Valid",
			Message:            "Pipeline spec is valid",
			LastTransitionTime: ltt,
		},
	}

	c := newClientWithIndex(newScheme(), p)

	r := &pipeline.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo", Namespace: "default"},
	})
	require.NoError(t, err)

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nginx-demo", Namespace: "default"},
	})
	require.NoError(t, err)

	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{
		Name: "nginx-demo", Namespace: "default",
	}, &got))

	require.Len(t, got.Status.Conditions, 1)
	assert.Equal(t, "Valid", got.Status.Conditions[0].Reason)
	assert.True(t, ltt.Equal(&got.Status.Conditions[0].LastTransitionTime), "lastTransitionTime must not move")
}

// TestPipelineReconciler_NotFound verifies that a missing Pipeline is handled
// gracefully (deleted between event and reconcile).
func TestPipelineReconciler_NotFound(t *testing.T) {
	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()

	r := &pipeline.Reconciler{Client: c}
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "gone", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

// TestDerivePhase_NoSteps verifies that no Bundles and no steps yield Unknown.
func TestDerivePhase_NoSteps(t *testing.T) {
	assert.Equal(t, "Unknown", pipeline.DerivePhase("app", nil, nil))
	assert.Equal(t, "Unknown", pipeline.DerivePhase("app", []kardinalv1alpha1.Bundle{}, []kardinalv1alpha1.PromotionStep{}))
}

// phaseBundle returns a Bundle of pipeline "app" in phase, created at.
func phaseBundle(name, phase string, at metav1.Time) kardinalv1alpha1.Bundle {
	return kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: at},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: phase},
	}
}

// TestDerivePhase_AllVerified verifies that all-Verified steps yield Ready.
func TestDerivePhase_AllVerified(t *testing.T) {
	now := metav1.Now()
	// A settled Bundle, so the steps alone decide.
	settled := []kardinalv1alpha1.Bundle{phaseBundle("b1", "Verified", now)}
	steps := []kardinalv1alpha1.PromotionStep{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s-test", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "b1", Environment: "test"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Verified"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s-prod", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "b1", Environment: "prod"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Verified"},
		},
	}
	assert.Equal(t, "Ready", pipeline.DerivePhase("app", settled, steps))
}

// TestDerivePhase_OneFailed verifies that a Failed step yields Degraded.
func TestDerivePhase_OneFailed(t *testing.T) {
	now := metav1.Now()
	// A settled Bundle, so the steps alone decide.
	settled := []kardinalv1alpha1.Bundle{phaseBundle("b1", "Verified", now)}
	steps := []kardinalv1alpha1.PromotionStep{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s-test", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "b1", Environment: "test"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Verified"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s-prod", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "b1", Environment: "prod"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Failed"},
		},
	}
	assert.Equal(t, "Degraded", pipeline.DerivePhase("app", settled, steps))
}

// TestDerivePhase_Promoting verifies that a step still in flight yields
// Promoting, not Unknown (E2E-R05).
func TestDerivePhase_Promoting(t *testing.T) {
	now := metav1.Now()
	// A settled Bundle, so the steps alone decide.
	settled := []kardinalv1alpha1.Bundle{phaseBundle("b1", "Verified", now)}
	steps := []kardinalv1alpha1.PromotionStep{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s-test", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "b1", Environment: "test"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Verified"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s-prod", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "b1", Environment: "prod"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Promoting"},
		},
	}
	assert.Equal(t, "Promoting", pipeline.DerivePhase("app", settled, steps))
}

// TestDerivePhase_MultiBundle_NewFailed_OldVerified verifies that when a new bundle
// has a Failed step, Degraded is shown (not Ready from the old Verified step).
func TestDerivePhase_MultiBundle_NewFailed_OldVerified(t *testing.T) {
	old := metav1.NewTime(metav1.Now().Add(-1 * 3600 * 1e9)) // 1 hour ago
	now := metav1.Now()
	steps := []kardinalv1alpha1.PromotionStep{
		// Old bundle — Verified in prod
		{
			ObjectMeta: metav1.ObjectMeta{Name: "old-prod", Namespace: "default", CreationTimestamp: old},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "old", Environment: "prod"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Verified"},
		},
		// New bundle — Failed in prod
		{
			ObjectMeta: metav1.ObjectMeta{Name: "new-prod", Namespace: "default", CreationTimestamp: now},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "new", Environment: "prod"},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: "Failed"},
		},
	}
	bundles := []kardinalv1alpha1.Bundle{phaseBundle("old", "Verified", old), phaseBundle("new", "Failed", now)}
	// The newest Bundle per env wins: new is Failed in prod → Degraded
	assert.Equal(t, "Degraded", pipeline.DerivePhase("app", bundles, steps))
}
