// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

func reconcilePipeline(t *testing.T, c client.Client, name string) (kardinalv1alpha1.Pipeline, error) {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}}
	_, err := (&pipeline.Reconciler{Client: c}).Reconcile(context.Background(), req)
	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	return got, err
}

// TestPipelineLifecycle_ReadyCondition covers C02-bundle-15 and E2E-05: a
// valid Pipeline is Ready=True after its Bundles verify, and a dependsOn cycle
// is a validation failure, not a healthy pipeline.
func TestPipelineLifecycle_ReadyCondition(t *testing.T) {
	now := time.Now().UTC()
	cyclic := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{
			{Name: "a", DependsOn: []string{"b"}},
			{Name: "b", DependsOn: []string{"a"}},
		}},
	}
	tests := []struct {
		name       string
		objs       []client.Object
		wantStatus metav1.ConditionStatus
		wantReason string
		wantMsg    string
	}{
		{
			name: "valid pipeline with every environment verified",
			objs: []client.Object{
				makePipelineWithEnvs("app", "default", "test", "prod"),
				makeVerifiedBundle("app-v1", "default", "app", now.Add(-2*time.Hour)),
				makeVerifiedStep("app-v1", "app", "test", "default", now.Add(-time.Hour)),
				makeVerifiedStep("app-v1", "app", "prod", "default", now.Add(-30*time.Minute)),
			},
			wantStatus: metav1.ConditionTrue, wantReason: "Valid",
		},
		{
			name:       "dependsOn cycle",
			objs:       []client.Object{cyclic},
			wantStatus: metav1.ConditionFalse, wantReason: "ValidationFailed", wantMsg: "circular dependency",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := reconcilePipeline(t, newClientWithIndex(newPipelineScheme(), tc.objs...), "app")
			require.NoError(t, err)
			ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			require.NotNil(t, ready)
			assert.Equal(t, tc.wantStatus, ready.Status)
			assert.Equal(t, tc.wantReason, ready.Reason)
			assert.Contains(t, ready.Message, tc.wantMsg)
		})
	}
}

// TestPipelineLifecycle_ListErrorKeepsStatus covers C02-bundle-16: a failed
// List returns the error and leaves the last good phase and metrics alone.
func TestPipelineLifecycle_ListErrorKeepsStatus(t *testing.T) {
	now := time.Now().UTC()
	objs := []client.Object{
		makePipelineWithEnvs("app", "default", "test", "prod"),
		makeVerifiedBundle("app-v1", "default", "app", now.Add(-2*time.Hour)),
		makeVerifiedStep("app-v1", "app", "test", "default", now.Add(-time.Hour)),
		makeVerifiedStep("app-v1", "app", "prod", "default", now.Add(-30*time.Minute)),
	}
	for _, failing := range []string{"PromotionStepList", "BundleList"} {
		t.Run(failing, func(t *testing.T) {
			failList := false
			c := fake.NewClientBuilder().WithScheme(newPipelineScheme()).WithObjects(objs...).
				WithStatusSubresource(&kardinalv1alpha1.Pipeline{}).
				WithIndex(&kardinalv1alpha1.PromotionStep{}, "spec.pipelineName", func(obj client.Object) []string {
					return []string{obj.(*kardinalv1alpha1.PromotionStep).Spec.PipelineName}
				}).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if failList && fmt.Sprintf("%T", list) == "*v1alpha1."+failing {
							return errors.New("etcdserver: request timed out")
						}
						return cl.List(ctx, list, opts...)
					},
				}).Build()

			before, err := reconcilePipeline(t, c, "app")
			require.NoError(t, err)
			require.Equal(t, "Ready", before.Status.Phase)
			require.NotNil(t, before.Status.DeploymentMetrics)

			failList = true
			after, err := reconcilePipeline(t, c, "app")
			require.ErrorContains(t, err, "request timed out", "the error is returned so the request is retried")
			assert.Equal(t, "Ready", after.Status.Phase)
			assert.Equal(t, before.Status.DeploymentMetrics, after.Status.DeploymentMetrics)
		})
	}
}

// TestPipelineLifecycle_RolloutsNotCapped covers C02-bundle-17: the 30-day
// rollout count covers every bundle, only the percentile sample is capped.
func TestPipelineLifecycle_RolloutsNotCapped(t *testing.T) {
	now := time.Now().UTC()
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	var bundles []kardinalv1alpha1.Bundle
	var steps []kardinalv1alpha1.PromotionStep
	for i := range 45 { // 45 prod deploys in the last ~11 days (4 per day)
		name := fmt.Sprintf("app-v%d", i)
		at := now.Add(-time.Duration(i) * 6 * time.Hour)
		bundles = append(bundles, *makeVerifiedBundle(name, "default", "app", at.Add(-time.Hour)))
		steps = append(steps, *makeVerifiedStep(name, "app", "prod", "default", at))
	}
	m := pipeline.ComputeDeploymentMetrics(p, bundles, steps, now)
	require.NotNil(t, m)
	assert.Equal(t, 45, m.RolloutsLast30Days)
	assert.Equal(t, 30, m.SampleSize, "the percentile sample stays capped")
	assert.Equal(t, 0, m.StaleProdDays)
}

// TestPipelineLifecycle_FinalEnvironmentIsSink covers C02-bundle-18: metrics
// follow the DAG sink named prod, not the environment listed last.
func TestPipelineLifecycle_FinalEnvironmentIsSink(t *testing.T) {
	now := time.Now().UTC()
	fanOut := func(envs ...kardinalv1alpha1.EnvironmentSpec) *kardinalv1alpha1.Pipeline {
		return &kardinalv1alpha1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
			Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
		}
	}
	tests := []struct {
		name     string
		pipeline *kardinalv1alpha1.Pipeline
		env      string
		wantNil  bool
	}{
		{name: "prod is a sink listed before a side branch",
			pipeline: fanOut(kardinalv1alpha1.EnvironmentSpec{Name: "test"}, kardinalv1alpha1.EnvironmentSpec{Name: "uat"},
				kardinalv1alpha1.EnvironmentSpec{Name: "prod", DependsOn: []string{"uat"}},
				kardinalv1alpha1.EnvironmentSpec{Name: "perf", DependsOn: []string{"uat"}}),
			env: "prod"},
		{name: "an intermediate environment is not final",
			pipeline: makePipelineWithEnvs("app", "default", "test", "uat", "live"),
			env:      "uat", wantNil: true},
		{name: "without a prod sink the last sink is final",
			pipeline: fanOut(kardinalv1alpha1.EnvironmentSpec{Name: "test"},
				kardinalv1alpha1.EnvironmentSpec{Name: "eu", DependsOn: []string{"test"}},
				kardinalv1alpha1.EnvironmentSpec{Name: "us", DependsOn: []string{"test"}}),
			env: "us"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := makeVerifiedBundle("app-v1", "default", "app", now.Add(-2*time.Hour))
			s := makeVerifiedStep("app-v1", "app", tc.env, "default", now.Add(-time.Hour))
			m := pipeline.ComputeDeploymentMetrics(tc.pipeline, []kardinalv1alpha1.Bundle{*b}, []kardinalv1alpha1.PromotionStep{*s}, now)
			if tc.wantNil {
				assert.Nil(t, m)
				return
			}
			require.NotNil(t, m)
			assert.Equal(t, 1, m.RolloutsLast30Days)
		})
	}
}

// TestPipelineLifecycle_DerivePhase covers C02-bundle-19: AbortedByAlarm and
// RollingBack are Degraded, and a failed region is never hidden by List order.
func TestPipelineLifecycle_DerivePhase(t *testing.T) {
	ts := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	later := metav1.NewTime(ts.Add(time.Minute))
	step := func(name, bundle, env, state string, at metav1.Time) kardinalv1alpha1.PromotionStep {
		return kardinalv1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: at},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: bundle, Environment: env},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: state},
		}
	}
	tests := []struct {
		name  string
		steps []kardinalv1alpha1.PromotionStep
		want  string
	}{
		{name: "AbortedByAlarm is degraded",
			steps: []kardinalv1alpha1.PromotionStep{step("v1-test", "v1", "test", "Verified", ts), step("v1-prod", "v1", "prod", "AbortedByAlarm", ts)},
			want:  "Degraded"},
		{name: "RollingBack is degraded",
			steps: []kardinalv1alpha1.PromotionStep{step("v1-prod", "v1", "prod", "RollingBack", ts)},
			want:  "Degraded"},
		{name: "a failed region listed second",
			steps: []kardinalv1alpha1.PromotionStep{step("v1-prod-us", "v1", "prod", "Verified", ts), step("v1-prod-eu", "v1", "prod", "Failed", ts)},
			want:  "Degraded"},
		{name: "a failed region listed first",
			steps: []kardinalv1alpha1.PromotionStep{step("v1-prod-eu", "v1", "prod", "Failed", ts), step("v1-prod-us", "v1", "prod", "Verified", ts)},
			want:  "Degraded"},
		{name: "a newer bundle verified replaces an old failure",
			steps: []kardinalv1alpha1.PromotionStep{step("v1-prod", "v1", "prod", "Failed", ts), step("v2-prod", "v2", "prod", "Verified", later)},
			want:  "Ready"},
		{name: "same-second bundles tie-break by bundle name in either order",
			steps: []kardinalv1alpha1.PromotionStep{step("b-prod", "b", "prod", "Verified", ts), step("a-prod", "a", "prod", "Failed", ts)},
			want:  "Ready"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// One settled Bundle per step Bundle, created with its step, so the
			// steps alone decide.
			var bundles []kardinalv1alpha1.Bundle
			seen := map[string]bool{}
			for _, s := range tc.steps {
				if !seen[s.Spec.BundleName] {
					seen[s.Spec.BundleName] = true
					bundles = append(bundles, kardinalv1alpha1.Bundle{
						ObjectMeta: metav1.ObjectMeta{Name: s.Spec.BundleName, CreationTimestamp: s.CreationTimestamp},
						Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
						Status:     kardinalv1alpha1.BundleStatus{Phase: "Verified"},
					})
				}
			}
			assert.Equal(t, tc.want, pipeline.DerivePhase("app", bundles, tc.steps))
			reversed := make([]kardinalv1alpha1.PromotionStep, len(tc.steps))
			for i := range tc.steps {
				reversed[len(tc.steps)-1-i] = tc.steps[i]
			}
			assert.Equal(t, tc.want, pipeline.DerivePhase("app", bundles, reversed), "the phase must not depend on List order")
		})
	}
}

// TestPipelineLifecycle_DerivePhaseInFlight covers E2E-R05: a Pipeline whose
// newest Bundle is still promoting, or is held by a gate before any step
// exists, is Promoting, not Unknown (shown as "Idle" in the UI). Unknown is
// left for a Pipeline with nothing in flight and no steps. The newest Bundle
// is chosen as the UI and the CLI choose it.
func TestPipelineLifecycle_DerivePhaseInFlight(t *testing.T) {
	ts := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	later := metav1.NewTime(ts.Add(time.Minute))
	bundle := func(name, pipelineName, phase string, at metav1.Time) kardinalv1alpha1.Bundle {
		return kardinalv1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: at},
			Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: pipelineName},
			Status:     kardinalv1alpha1.BundleStatus{Phase: phase},
		}
	}
	step := func(bundleName, env, state string, at metav1.Time) kardinalv1alpha1.PromotionStep {
		return kardinalv1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: bundleName + "-" + env, CreationTimestamp: at},
			Spec:       kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: bundleName, Environment: env},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: state},
		}
	}
	createdAt := func(b kardinalv1alpha1.Bundle, at time.Time) kardinalv1alpha1.Bundle {
		b.Annotations = map[string]string{lifecycle.AnnotationCreatedAt: at.Format(time.RFC3339Nano)}
		return b
	}
	verifiedV1 := []kardinalv1alpha1.PromotionStep{step("v1", "test", "Verified", ts), step("v1", "prod", "Verified", ts)}
	tests := []struct {
		name    string
		bundles []kardinalv1alpha1.Bundle
		steps   []kardinalv1alpha1.PromotionStep
		want    string
	}{
		{name: "no bundles and no steps", want: "Unknown"},
		{name: "a gate holds the only bundle before any step exists",
			bundles: []kardinalv1alpha1.Bundle{bundle("v1", "app", "Promoting", ts)}, want: "Promoting"},
		{name: "a bundle waiting for a maxConcurrentPromotions slot",
			bundles: []kardinalv1alpha1.Bundle{bundle("v1", "app", "Available", ts)}, want: "Promoting"},
		{name: "a bundle the bundle reconciler has not seen yet",
			bundles: []kardinalv1alpha1.Bundle{bundle("v1", "app", "", ts)}, want: "Promoting"},
		{name: "the newest step is still promoting",
			bundles: []kardinalv1alpha1.Bundle{bundle("v1", "app", "Promoting", ts)},
			steps:   []kardinalv1alpha1.PromotionStep{step("v1", "test", "Verified", ts), step("v1", "prod", "WaitingForMerge", ts)},
			want:    "Promoting"},
		{name: "a gate holds a newer bundle while the old one is verified everywhere",
			bundles: []kardinalv1alpha1.Bundle{bundle("v1", "app", "Verified", ts), bundle("v2", "app", "Promoting", later)},
			steps:   verifiedV1, want: "Promoting"},
		{name: "every bundle settled and every newest step verified",
			bundles: []kardinalv1alpha1.Bundle{bundle("v0", "app", "Superseded", ts), bundle("v1", "app", "Verified", later)},
			steps:   verifiedV1, want: "Ready"},
		{name: "another pipeline's bundle in flight is ignored",
			bundles: []kardinalv1alpha1.Bundle{bundle("v1", "app", "Verified", ts), bundle("o1", "other", "Promoting", later)},
			steps:   verifiedV1, want: "Ready"},
		{name: "a failed step wins over a bundle in flight",
			bundles: []kardinalv1alpha1.Bundle{bundle("v1", "app", "Failed", ts), bundle("v2", "app", "Promoting", later)},
			steps:   []kardinalv1alpha1.PromotionStep{step("v1", "test", "Failed", ts)},
			want:    "Degraded"},
		{name: "the newest bundle failed before any step was created",
			bundles: []kardinalv1alpha1.Bundle{bundle("v1", "app", "Verified", ts), bundle("v2", "app", "Failed", later)},
			steps:   verifiedV1, want: "Degraded"},
		{name: "an old failed bundle replaced by a verified one",
			bundles: []kardinalv1alpha1.Bundle{bundle("v0", "app", "Failed", ts), bundle("v1", "app", "Verified", later)},
			steps:   verifiedV1, want: "Ready"},
		// Superseded Bundles and their steps are skipped, as the UI and the CLI
		// pick the current Bundle: handleSuperseded fails the old Bundle's
		// cancelled prod step, which must not make the Pipeline Degraded.
		{name: "a superseded bundle's cancelled step is skipped",
			bundles: []kardinalv1alpha1.Bundle{bundle("v1", "app", "Superseded", ts), bundle("v2", "app", "Promoting", later)},
			steps: []kardinalv1alpha1.PromotionStep{step("v1", "test", "Verified", ts), step("v1", "prod", "Failed", ts),
				step("v2", "test", "Promoting", later)},
			want: "Promoting"},
		// Newest is lifecycle.CompareCreation: in the same second the
		// created-at annotation decides, not the name or the step's own time.
		{name: "a same-second tie follows the created-at annotation",
			bundles: []kardinalv1alpha1.Bundle{
				createdAt(bundle("a", "app", "Verified", ts), ts.Add(500*time.Millisecond)),
				createdAt(bundle("b", "app", "Failed", ts), ts.Add(100*time.Millisecond)),
			},
			steps: []kardinalv1alpha1.PromotionStep{step("a", "prod", "Verified", ts), step("b", "prod", "Failed", later)},
			want:  "Ready"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, pipeline.DerivePhase("app", tc.bundles, tc.steps))
			reversed := make([]kardinalv1alpha1.Bundle, len(tc.bundles))
			for i := range tc.bundles {
				reversed[len(tc.bundles)-1-i] = tc.bundles[i]
			}
			assert.Equal(t, tc.want, pipeline.DerivePhase("app", reversed, tc.steps), "the phase must not depend on List order")
		})
	}
}

// TestPipelineLifecycle_PhasePromotingWhileGateBlocks covers E2E-R05 end to
// end: the reconciler writes Promoting for a Pipeline whose only Bundle is held
// by a gate and has no PromotionStep yet.
func TestPipelineLifecycle_PhasePromotingWhileGateBlocks(t *testing.T) {
	b := makeVerifiedBundle("app-v1", "default", "app", time.Now().UTC())
	b.Status.Phase = "Promoting"
	got, err := reconcilePipeline(t, newClientWithIndex(newPipelineScheme(),
		makePipelineWithEnvs("app", "default", "prod"), b), "app")
	require.NoError(t, err)
	assert.Equal(t, "Promoting", got.Status.Phase)
}

// TestPipelineLifecycle_FreezeGateFollowsSpecPaused covers C02-bundle-08: the
// Pipeline reconciler converges the freeze gate to spec.paused, so a plain spec
// edit pauses and resumes, and a user gate of the same name is left alone.
func TestPipelineLifecycle_FreezeGateFollowsSpecPaused(t *testing.T) {
	ctx := context.Background()
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.UID = "uid-app"
	p.Spec.Paused = true
	c := newClientWithIndex(newPipelineScheme(), p)
	gateKey := types.NamespacedName{Namespace: "default", Name: lifecycle.FreezeGateName("app")}

	_, err := reconcilePipeline(t, c, "app")
	require.NoError(t, err)
	var gate kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(ctx, gateKey, &gate), "a paused pipeline gets its freeze gate")
	assert.Equal(t, "true", gate.Labels[lifecycle.LabelFreeze])
	require.Len(t, gate.OwnerReferences, 1)
	assert.Equal(t, "app", gate.OwnerReferences[0].Name)

	_, err = reconcilePipeline(t, c, "app")
	require.NoError(t, err, "a second reconcile is a no-op")

	var cur kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "app"}, &cur))
	cur.Spec.Paused = false
	require.NoError(t, c.Update(ctx, &cur))
	_, err = reconcilePipeline(t, c, "app")
	require.NoError(t, err)
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, gateKey, &gate)), "resume removes the freeze gate")

	user := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: gateKey.Name, Namespace: "default"},
		Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: "true"},
	}
	require.NoError(t, c.Create(ctx, user))
	_, err = reconcilePipeline(t, c, "app")
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, gateKey, &gate), "a user gate without the freeze label is not deleted")
}

// TestPipelineLifecycle_PausedCondition: a paused Pipeline reports Paused=True
// while its freeze gate holds it. A user PolicyGate named freeze-<pipeline>
// used to disable the pause silently; it is now Paused=False with reason
// FreezeGateNameConflict (no error, no retry loop), and the pause takes effect
// once the user gate is gone. Resume removes the condition.
func TestPipelineLifecycle_PausedCondition(t *testing.T) {
	ctx := context.Background()
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.UID = "uid-app"
	p.Spec.Paused = true
	user := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: lifecycle.FreezeGateName("app"), Namespace: "default"},
		Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: "true"},
	}
	c := newClientWithIndex(newPipelineScheme(), p, user)
	gateKey := types.NamespacedName{Namespace: "default", Name: lifecycle.FreezeGateName("app")}

	got, err := reconcilePipeline(t, c, "app")
	require.NoError(t, err, "a name conflict is reported, not retried")
	cond := meta.FindStatusCondition(got.Status.Conditions, "Paused")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "FreezeGateNameConflict", cond.Reason)
	assert.Contains(t, cond.Message, "freeze-app")
	var gate kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(ctx, gateKey, &gate))
	assert.Equal(t, "true", gate.Spec.Expression, "the user's gate is not changed")
	paused, err := lifecycle.IsPaused(ctx, c, "default", "app")
	require.NoError(t, err)
	assert.False(t, paused)

	again, err := reconcilePipeline(t, c, "app")
	require.NoError(t, err)
	assert.Equal(t, got.ResourceVersion, again.ResourceVersion, "a second reconcile writes nothing")

	require.NoError(t, c.Delete(ctx, &gate))
	got, err = reconcilePipeline(t, c, "app")
	require.NoError(t, err)
	cond = meta.FindStatusCondition(got.Status.Conditions, "Paused")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "FreezeGateActive", cond.Reason)
	paused, err = lifecycle.IsPaused(ctx, c, "default", "app")
	require.NoError(t, err)
	assert.True(t, paused, "with the user gate gone the pause takes effect")

	got.Spec.Paused = false
	require.NoError(t, c.Update(ctx, &got))
	got, err = reconcilePipeline(t, c, "app")
	require.NoError(t, err)
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, "Paused"), "resume removes the condition")
	require.NotNil(t, meta.FindStatusCondition(got.Status.Conditions, "Ready"))
}
