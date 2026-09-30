// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Regression tests for the Bundle lifecycle findings of the audit (chunk
// C02-bundle, C13b-design-07, E2E-09): phase derivation, recovery,
// supersession order, the concurrency cap, Graph conditions and the watches.
package bundle_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
)

// --- fixtures ---

func lcClient(objs ...client.Object) client.Client {
	return indexedBuilder(newScheme()).
		WithObjects(objs...).
		WithStatusSubresource(&kardinalv1alpha1.Bundle{}, &kardinalv1alpha1.Pipeline{}, &kardinalv1alpha1.PromotionStep{}).
		Build()
}

func lcPipeline(name string, envs ...kardinalv1alpha1.EnvironmentSpec) *kardinalv1alpha1.Pipeline {
	return &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
	}
}

func lcEnvs(names ...string) []kardinalv1alpha1.EnvironmentSpec {
	out := make([]kardinalv1alpha1.EnvironmentSpec, 0, len(names))
	for _, n := range names {
		out = append(out, kardinalv1alpha1.EnvironmentSpec{Name: n})
	}
	return out
}

func lcBundle(name, typ, phase string, created time.Time) *kardinalv1alpha1.Bundle {
	return &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: metav1.NewTime(created)},
		Spec:       kardinalv1alpha1.BundleSpec{Type: typ, Pipeline: "app"},
		Status:     kardinalv1alpha1.BundleStatus{Phase: phase},
	}
}

func lcStep(bundleName, env, name, state string) *kardinalv1alpha1.PromotionStep {
	return &kardinalv1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/bundle": bundleName},
		},
		Spec: kardinalv1alpha1.PromotionStepSpec{
			PipelineName: "app", BundleName: bundleName, Environment: env, StepType: "auto",
		},
		Status: kardinalv1alpha1.PromotionStepStatus{State: state},
	}
}

func lcReconcile(t *testing.T, r *bundle.Reconciler, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: "default"},
	})
	require.NoError(t, err)
	return res
}

func lcGet(t *testing.T, c client.Client, name string) kardinalv1alpha1.Bundle {
	t.Helper()
	var b kardinalv1alpha1.Bundle
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &b))
	return b
}

func lcSetStepState(t *testing.T, c client.Client, name, state string) {
	t.Helper()
	var ps kardinalv1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &ps))
	ps.Status.State = state
	require.NoError(t, c.Status().Update(context.Background(), &ps))
}

type countingTranslator struct{ calls int }

func (m *countingTranslator) Translate(_ context.Context, _ *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) (string, error) {
	m.calls++
	return "app-" + b.Name, nil
}

type flakyTranslator struct{ calls int }

func (m *flakyTranslator) Translate(_ context.Context, _ *kardinalv1alpha1.Pipeline, _ *kardinalv1alpha1.Bundle) (string, error) {
	m.calls++
	if m.calls == 1 {
		return "", errors.New("translator.Translate: collect gates: etcdserver: request timed out")
	}
	return "app-app-v1", nil
}

type existsChecker struct{}

func (existsChecker) GraphExists(_ context.Context, _, _ string) (bool, error) { return true, nil }

// graphStatusReader is a GraphChecker that also returns the Graph with status,
// like *graph.GraphClient.
type graphStatusReader struct {
	g    *graph.Graph
	gets int
}

func (m *graphStatusReader) GraphExists(_ context.Context, _, _ string) (bool, error) {
	return m.g != nil, nil
}

func (m *graphStatusReader) Get(_ context.Context, _, name string) (*graph.Graph, error) {
	m.gets++
	if m.g == nil {
		return nil, fmt.Errorf("get graph: %w", apierrors.NewNotFound(schema.GroupResource{Group: "kro.run", Resource: "graphs"}, name))
	}
	return m.g, nil
}

// --- phase derivation ---

// C02-bundle-01: every environment Verified makes the Bundle Verified.
func TestLifecycle_AllEnvironmentsVerifiedMakesBundleVerified(t *testing.T) {
	b := lcBundle("app-v1", "image", "Promoting", time.Now().UTC().Add(-time.Hour))
	c := lcClient(lcPipeline("app", lcEnvs("test", "prod")...), b,
		lcStep("app-v1", "test", "s-test", "Verified"),
		lcStep("app-v1", "prod", "s-prod", "Verified"))
	r := &bundle.Reconciler{Client: c}
	res := lcReconcile(t, r, "app-v1")
	lcReconcile(t, r, "app-v1")

	got := lcGet(t, c, "app-v1")
	assert.Equal(t, "Verified", got.Status.Phase)
	require.NotNil(t, got.Status.Metrics)
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, "Verified", ready.Reason)
	assert.Zero(t, res.RequeueAfter, "a Verified bundle has no soak to tick")
}

// C02-bundle-20: status.metrics.bakeResets is the sum of the steps' bake
// resets, not always 0.
func TestLifecycle_MetricsSumBakeResets(t *testing.T) {
	b := lcBundle("app-v1", "image", "Promoting", time.Now().UTC().Add(-time.Hour))
	test, prod := lcStep("app-v1", "test", "s-test", "Verified"), lcStep("app-v1", "prod", "s-prod", "Verified")
	test.Status.BakeResets, prod.Status.BakeResets = 1, 2
	c := lcClient(lcPipeline("app", lcEnvs("test", "prod")...), b, test, prod)
	r := &bundle.Reconciler{Client: c}
	lcReconcile(t, r, "app-v1")
	lcReconcile(t, r, "app-v1")

	got := lcGet(t, c, "app-v1")
	require.NotNil(t, got.Status.Metrics)
	assert.Equal(t, 3, got.Status.Metrics.BakeResets)
}

// #1308: status.metrics.operatorInterventions counts the overrides recorded
// on the Bundle's own gate instances, not always 0.
func TestLifecycle_MetricsCountOperatorInterventions(t *testing.T) {
	gate := func(name, bundleName string, overrides int) *kardinalv1alpha1.PolicyGate {
		g := &kardinalv1alpha1.PolicyGate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				Labels:    map[string]string{"kardinal.io/bundle": bundleName, "kardinal.io/pipeline": "app"},
			},
			Spec: kardinalv1alpha1.PolicyGateSpec{Expression: "false"},
		}
		for i := 0; i < overrides; i++ {
			g.Spec.Overrides = append(g.Spec.Overrides, kardinalv1alpha1.PolicyGateOverride{
				Reason:    fmt.Sprintf("hotfix %d", i),
				ExpiresAt: metav1.NewTime(time.Now().UTC().Add(time.Hour)),
			})
		}
		return g
	}
	tests := []struct {
		name  string
		gates []client.Object
		want  int
	}{
		{name: "no gate instances", want: 0},
		{name: "gate instances without overrides", gates: []client.Object{
			gate("g-test", "app-v1", 0), gate("g-prod", "app-v1", 0),
		}, want: 0},
		{name: "two overrides on one gate", gates: []client.Object{
			gate("g-test", "app-v1", 0), gate("g-prod", "app-v1", 2),
		}, want: 2},
		{name: "overrides on two gates add up", gates: []client.Object{
			gate("g-test", "app-v1", 1), gate("g-prod", "app-v1", 2),
		}, want: 3},
		{name: "another Bundle's gates are not counted", gates: []client.Object{
			gate("g-prod", "app-v1", 1), gate("g-prod-v0", "app-v0", 4), gate("g-other", "other-v1", 2),
		}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := lcBundle("app-v1", "image", "Promoting", time.Now().UTC().Add(-time.Hour))
			objs := append([]client.Object{
				lcPipeline("app", lcEnvs("test", "prod")...), b,
				lcStep("app-v1", "test", "s-test", "Verified"),
				lcStep("app-v1", "prod", "s-prod", "Verified"),
			}, tt.gates...)
			c := lcClient(objs...)
			r := &bundle.Reconciler{Client: c}
			lcReconcile(t, r, "app-v1")
			lcReconcile(t, r, "app-v1")

			got := lcGet(t, c, "app-v1")
			require.Equal(t, "Verified", got.Status.Phase)
			require.NotNil(t, got.Status.Metrics)
			assert.Equal(t, tt.want, got.Status.Metrics.OperatorInterventions)
		})
	}
}

// #1308: a failed read of the gate instances is retried, not recorded as zero
// interventions in metrics that are written only once.
func TestLifecycle_MetricsNotWrittenWhenGateListFails(t *testing.T) {
	b := lcBundle("app-v1", "image", "Promoting", time.Now().UTC().Add(-time.Hour))
	c := indexedBuilder(newScheme()).
		WithObjects(lcPipeline("app", lcEnvs("test")...), b, lcStep("app-v1", "test", "s-test", "Verified")).
		WithStatusSubresource(&kardinalv1alpha1.Bundle{}, &kardinalv1alpha1.Pipeline{}, &kardinalv1alpha1.PromotionStep{}).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*kardinalv1alpha1.PolicyGateList); ok {
					return apierrors.NewServiceUnavailable("etcd leader change")
				}
				return cl.List(ctx, list, opts...)
			},
		}).Build()
	r := &bundle.Reconciler{Client: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"},
	})
	_, err2 := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"},
	})
	require.Error(t, errors.Join(err, err2), "a failed read of the gate instances is retried")

	got := lcGet(t, c, "app-v1")
	assert.Nil(t, got.Status.Metrics)
	assert.NotEqual(t, "Verified", got.Status.Phase)
}

// C02-bundle-01: a Verified bundle stays Verified when a newer one arrives;
// the history keeps the successful promotion.
func TestLifecycle_VerifiedBundleIsNotSuperseded(t *testing.T) {
	t0 := time.Now().UTC().Add(-2 * time.Hour)
	c := lcClient(lcPipeline("app", lcEnvs("test")...), lcBundle("app-v1", "image", "Promoting", t0),
		lcStep("app-v1", "test", "s-test", "Verified"))
	r := &bundle.Reconciler{Client: c}
	lcReconcile(t, r, "app-v1")
	require.Equal(t, "Verified", lcGet(t, c, "app-v1").Status.Phase)

	require.NoError(t, c.Create(context.Background(), lcBundle("app-v2", "image", "Available", t0.Add(time.Hour))))
	lcReconcile(t, r, "app-v1")
	assert.Equal(t, "Verified", lcGet(t, c, "app-v1").Status.Phase)
}

// C02-bundle-02 / E2E-09: a failed step (for example a closed PR) fails the
// Bundle with the step's message.
func TestLifecycle_FailedStepFailsBundle(t *testing.T) {
	step := lcStep("app-v1", "test", "s-test", "Failed")
	step.Status.Message = "PR #12 was closed without merging"
	c := lcClient(lcPipeline("app", lcEnvs("test", "prod")...),
		lcBundle("app-v1", "image", "Promoting", time.Now().UTC()), step)
	r := &bundle.Reconciler{Client: c}
	lcReconcile(t, r, "app-v1")

	got := lcGet(t, c, "app-v1")
	assert.Equal(t, "Failed", got.Status.Phase)
	failed := meta.FindStatusCondition(got.Status.Conditions, "Failed")
	require.NotNil(t, failed)
	assert.Equal(t, metav1.ConditionTrue, failed.Status)
	assert.Equal(t, "StepFailed", failed.Reason)
	assert.Contains(t, failed.Message, "environment test")
	assert.Contains(t, failed.Message, "closed without merging")
	assert.True(t, meta.IsStatusConditionFalse(got.Status.Conditions, "Ready"))
}

// C02-bundle-02: AbortedByAlarm and RollingBack are failures too.
func TestLifecycle_AbortedAndRollingBackFailBundle(t *testing.T) {
	for _, state := range []string{"AbortedByAlarm", "RollingBack"} {
		t.Run(state, func(t *testing.T) {
			c := lcClient(lcPipeline("app", lcEnvs("test")...),
				lcBundle("app-v1", "image", "Promoting", time.Now().UTC()),
				lcStep("app-v1", "test", "s-test", state))
			lcReconcile(t, &bundle.Reconciler{Client: c}, "app-v1")
			assert.Equal(t, "Failed", lcGet(t, c, "app-v1").Status.Phase)
		})
	}
}

// C02-bundle-05: a Failed Bundle recovers when its step is retried.
func TestLifecycle_FailedBundleRecoversWhenStepRetried(t *testing.T) {
	b := lcBundle("app-v1", "image", "Promoting", time.Now().UTC())
	b.Status.GraphRef = "app-app-v1" // a Graph exists, so the failure is a step's
	c := lcClient(lcPipeline("app", lcEnvs("test")...), b,
		lcStep("app-v1", "test", "s-test", "Failed"))
	r := &bundle.Reconciler{Client: c}
	lcReconcile(t, r, "app-v1")
	require.Equal(t, "Failed", lcGet(t, c, "app-v1").Status.Phase)

	lcSetStepState(t, c, "s-test", "Promoting") // kardinal step retry
	lcReconcile(t, r, "app-v1")
	got := lcGet(t, c, "app-v1")
	assert.Equal(t, "Promoting", got.Status.Phase)
	failed := meta.FindStatusCondition(got.Status.Conditions, "Failed")
	require.NotNil(t, failed)
	assert.Equal(t, metav1.ConditionFalse, failed.Status)
	assert.Equal(t, "Recovered", failed.Reason)

	lcSetStepState(t, c, "s-test", "Verified")
	lcReconcile(t, r, "app-v1")
	assert.Equal(t, "Verified", lcGet(t, c, "app-v1").Status.Phase)
}

// C02-bundle-05: a Failed Bundle does not come back when a newer Bundle of
// the same type is in flight or Verified; it is Superseded instead.
func TestLifecycle_FailedBundleWithNewerSiblingIsSuperseded(t *testing.T) {
	t0 := time.Now().UTC().Add(-time.Hour)
	v1 := lcBundle("app-v1", "image", "Failed", t0)
	v1.Status.GraphRef = "app-app-v1"
	c := lcClient(lcPipeline("app", lcEnvs("test")...), v1,
		lcBundle("app-v2", "image", "Verified", t0.Add(time.Minute)),
		lcStep("app-v1", "test", "s-test", "Promoting"))
	lcReconcile(t, &bundle.Reconciler{Client: c}, "app-v1")
	assert.Equal(t, "Superseded", lcGet(t, c, "app-v1").Status.Phase)
}

// C02-bundle-05: a Bundle failed by an invalid Pipeline is retried when the
// Pipeline is corrected. The InvalidSpec message is the graph package's cycle
// error, which names each edge, so a wave cycle is not reported as a
// dependsOn cycle.
func TestLifecycle_InvalidPipelineRetriedWhenCorrected(t *testing.T) {
	tests := []struct {
		name     string
		envs     []kardinalv1alpha1.EnvironmentSpec
		contains []string
	}{
		{name: "dependsOn cycle",
			envs: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "a", DependsOn: []string{"b"}},
				{Name: "b", DependsOn: []string{"a"}},
			},
			contains: []string{"a dependsOn b", "b dependsOn a"}},
		{name: "waves listed out of order",
			envs: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"}, {Name: "a", Wave: 2}, {Name: "staging"}, {Name: "b", Wave: 1},
			},
			contains: []string{
				"a (wave 2) waits for all of wave 1, which includes b",
				"list the waves in ascending order",
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := lcClient(lcPipeline("app", tc.envs...), lcBundle("app-v1", "image", "Available", time.Now().UTC()))
			tr := &countingTranslator{}
			r := &bundle.Reconciler{Client: c, Translator: tr}
			lcReconcile(t, r, "app-v1")

			got := lcGet(t, c, "app-v1")
			assert.Equal(t, "Failed", got.Status.Phase)
			inv := meta.FindStatusCondition(got.Status.Conditions, "InvalidSpec")
			require.NotNil(t, inv)
			assert.Equal(t, "CircularDependency", inv.Reason)
			for _, want := range tc.contains {
				assert.Contains(t, inv.Message, want)
			}
			assert.NotContains(t, inv.Message, "circular dependsOn",
				"the message is the graph error, not a dependsOn-only summary")
			assert.Contains(t, inv.Message, "apply a corrected Pipeline to retry")
			assert.Zero(t, tr.calls, "an invalid Pipeline is not translated")

			// Nothing changed: the Bundle stays Failed.
			lcReconcile(t, r, "app-v1")
			assert.Equal(t, "Failed", lcGet(t, c, "app-v1").Status.Phase)

			var pl kardinalv1alpha1.Pipeline
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "app", Namespace: "default"}, &pl))
			pl.Spec.Environments = lcEnvs("a", "b")
			require.NoError(t, c.Update(context.Background(), &pl))
			for range 2 {
				lcReconcile(t, r, "app-v1")
			}
			got = lcGet(t, c, "app-v1")
			assert.Equal(t, "Promoting", got.Status.Phase)
			assert.Equal(t, 1, tr.calls)
			assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, "InvalidSpec"))
		})
	}
}

// C02-bundle-05: a transient translate error keeps the Bundle Available and
// is retried.
func TestLifecycle_TransientTranslateErrorIsRetried(t *testing.T) {
	c := lcClient(lcPipeline("app", lcEnvs("test")...), lcBundle("app-v1", "image", "Available", time.Now().UTC()))
	tr := &flakyTranslator{}
	r := &bundle.Reconciler{Client: c, Translator: tr}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}})
	require.Error(t, err)
	got := lcGet(t, c, "app-v1")
	assert.Equal(t, "Available", got.Status.Phase)
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, ready)
	assert.Equal(t, "TranslationError", ready.Reason)

	lcReconcile(t, r, "app-v1")
	assert.Equal(t, "Promoting", lcGet(t, c, "app-v1").Status.Phase)
}

// errTranslator returns err while it is set and counts its calls.
type errTranslator struct {
	err   error
	calls int
}

func (m *errTranslator) Translate(_ context.Context, _ *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) (string, error) {
	m.calls++
	if m.err != nil {
		return "", m.err
	}
	return "app-" + b.Name, nil
}

// C02-bundle-05, C02-bundle-10: a Translate error that wraps graph.ErrInvalid
// (a denied skip, custom steps, an invalid name or node ID) is permanent, so
// it fails the Bundle with InvalidSpec instead of retrying forever, on each
// path that translates: the first Graph (Available), the re-translate after a
// Pipeline change, and the recreate of a deleted Graph. The Bundle stays
// Failed without re-translating until something changes, and recovers once
// the Graph builds.
func TestLifecycle_InvalidGraphBuildFailsBundle(t *testing.T) {
	buildErr := fmt.Errorf("translator.Translate: build: %w",
		fmt.Errorf("skip denied for environment %q: %w", "uat", graph.ErrInvalid))
	tests := []struct {
		name  string
		phase string
		// hash is the stored PipelineSpecHash; "" for a Bundle not yet translated.
		hash    string
		checker bundle.GraphChecker
		// changePipeline is true when the fix is a Pipeline change; otherwise
		// the next reconcile retries (the recreate path translates each time).
		changePipeline bool
		// wantCalls is the total number of Translate calls at the end.
		wantCalls int
	}{
		{name: "first graph of an available bundle", phase: "Available",
			checker: existsChecker{}, changePipeline: true, wantCalls: 2},
		{name: "re-translate after a pipeline change", phase: "Promoting", hash: "hash-of-an-older-spec",
			checker: existsChecker{}, changePipeline: true, wantCalls: 2},
		// graphStatusReader{} never finds the Graph, so each of the four
		// reconciles recreates it.
		{name: "recreate of a deleted graph", phase: "Promoting",
			checker: &graphStatusReader{}, wantCalls: 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			b := lcBundle("app-v1", "image", tc.phase, time.Now().UTC())
			if tc.phase != "Available" {
				b.Status.GraphRef = "app-app-v1"
				b.Status.PipelineSpecHash = tc.hash
			}
			c := lcClient(lcPipeline("app", lcEnvs("test", "uat")...), b)
			tr := &errTranslator{err: buildErr}
			r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: tc.checker}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}}

			res, err := r.Reconcile(ctx, req)
			require.NoError(t, err, "a permanent build error is not retried with backoff")
			assert.Zero(t, res.RequeueAfter, "a permanent build error is not requeued")
			require.Equal(t, 1, tr.calls)
			got := lcGet(t, c, "app-v1")
			assert.Equal(t, "Failed", got.Status.Phase)
			inv := meta.FindStatusCondition(got.Status.Conditions, "InvalidSpec")
			require.NotNil(t, inv)
			assert.Equal(t, metav1.ConditionTrue, inv.Status)
			assert.Equal(t, "GraphBuildFailed", inv.Reason)
			assert.Contains(t, inv.Message, `skip denied for environment "uat"`)
			ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			require.NotNil(t, ready)
			assert.Equal(t, "Failed", ready.Reason)
			assert.NotEqual(t, "hash-of-an-older-spec", got.Status.PipelineSpecHash,
				"the hash of the failed spec is stored, so a Pipeline change retries")
			if tc.phase != "Available" {
				synced := meta.FindStatusCondition(got.Status.Conditions, "GraphSynced")
				require.NotNil(t, synced)
				assert.Equal(t, metav1.ConditionFalse, synced.Status)
				assert.Equal(t, "InvalidSpec", synced.Reason)
			}

			// Nothing changed: the Bundle stays Failed and is not rewritten.
			rv := got.ResourceVersion
			lcReconcile(t, r, "app-v1")
			got = lcGet(t, c, "app-v1")
			assert.Equal(t, "Failed", got.Status.Phase, "an invalid build does not recover by itself")
			assert.Equal(t, rv, got.ResourceVersion)
			if tc.changePipeline {
				assert.Equal(t, 1, tr.calls, "the same spec is not translated again")
			}

			// The user fixes the cause; the Graph builds and the Bundle promotes.
			tr.err = nil
			if tc.changePipeline {
				var pl kardinalv1alpha1.Pipeline
				require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "app", Namespace: "default"}, &pl))
				pl.Spec.Environments = lcEnvs("test", "uat", "prod")
				require.NoError(t, c.Update(ctx, &pl))
			}
			for range 2 {
				lcReconcile(t, r, "app-v1")
			}
			got = lcGet(t, c, "app-v1")
			assert.Equal(t, "Promoting", got.Status.Phase)
			assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, "InvalidSpec"))
			assert.Equal(t, tc.wantCalls, tr.calls)
		})
	}
}

// C02-bundle-06: a Bundle with intent.targetEnvironment finishes when the
// environments up to the target are Verified.
func TestLifecycle_TargetEnvironmentBundleFinishes(t *testing.T) {
	b := lcBundle("app-rollback-x", "image", "Promoting", time.Now().UTC().Add(-time.Hour))
	b.Spec.Intent = &kardinalv1alpha1.BundleIntent{TargetEnvironment: "uat"}
	c := lcClient(lcPipeline("app", lcEnvs("test", "uat", "prod")...), b,
		lcStep("app-rollback-x", "test", "s-test", "Verified"),
		lcStep("app-rollback-x", "uat", "s-uat", "Verified"))
	r := &bundle.Reconciler{Client: c}
	lcReconcile(t, r, "app-rollback-x")
	res := lcReconcile(t, r, "app-rollback-x")
	got := lcGet(t, c, "app-rollback-x")
	assert.Equal(t, "Verified", got.Status.Phase)
	assert.NotNil(t, got.Status.Metrics)
	assert.Zero(t, res.RequeueAfter)
}

// C02-bundle-07: a multi-region environment is Verified only when every
// region is.
func TestLifecycle_MultiRegionNeedsEveryRegion(t *testing.T) {
	p := lcPipeline("app",
		kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", Regions: []string{"us-east-1", "eu-west-1"}})
	c := lcClient(p, lcBundle("app-v1", "image", "Promoting", time.Now().UTC().Add(-time.Hour)),
		lcStep("app-v1", "test", "app-v1-test", "Verified"),
		lcStep("app-v1", "prod", "app-v1-prod-us-east-1", "Verified"),
		lcStep("app-v1", "prod", "app-v1-prod-eu-west-1", "WaitingForMerge"))
	r := &bundle.Reconciler{Client: c}
	lcReconcile(t, r, "app-v1")
	got := lcGet(t, c, "app-v1")
	assert.Equal(t, "Promoting", got.Status.Phase)
	assert.Nil(t, got.Status.Metrics)
	var prod *kardinalv1alpha1.EnvironmentStatus
	for i := range got.Status.Environments {
		if got.Status.Environments[i].Name == "prod" {
			prod = &got.Status.Environments[i]
		}
	}
	require.NotNil(t, prod)
	assert.Equal(t, "WaitingForMerge", prod.Phase)
	assert.Nil(t, prod.HealthCheckedAt)

	lcSetStepState(t, c, "app-v1-prod-eu-west-1", "Verified")
	lcReconcile(t, r, "app-v1")
	assert.Equal(t, "Verified", lcGet(t, c, "app-v1").Status.Phase)
}

// C02-bundle-12: status.environments is written in promotion order.
func TestLifecycle_EnvironmentsInPromotionOrder(t *testing.T) {
	names := []string{"dev", "test", "qa", "uat", "staging", "perf", "canary", "prod"}
	for range 10 {
		objs := []client.Object{lcPipeline("app", lcEnvs(names...)...),
			lcBundle("app-v1", "image", "Promoting", time.Now().UTC())}
		for _, n := range names {
			objs = append(objs, lcStep("app-v1", n, "s-"+n, "Promoting"))
		}
		c := lcClient(objs...)
		lcReconcile(t, &bundle.Reconciler{Client: c}, "app-v1")
		var got []string
		for _, e := range lcGet(t, c, "app-v1").Status.Environments {
			got = append(got, e.Name)
		}
		require.Equal(t, strings.Join(names, ","), strings.Join(got, ","))
	}
}

// C02-bundle-13: a reconcile that changes nothing keeps Ready's
// LastTransitionTime.
func TestLifecycle_ReadyTransitionTimeKept(t *testing.T) {
	old := metav1.NewTime(time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second))
	vt := metav1.NewTime(time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second))
	b := lcBundle("app-v1", "image", "Verified", time.Now().UTC().Add(-4*time.Hour))
	b.Status.Metrics = &kardinalv1alpha1.BundleMetrics{CommitToProductionMinutes: 60}
	b.Status.Environments = []kardinalv1alpha1.EnvironmentStatus{{Name: "test", Phase: "Verified", HealthCheckedAt: &vt, SoakMinutes: 119}}
	b.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Verified",
		Message: "all environments verified", LastTransitionTime: old}}
	c := lcClient(lcPipeline("app", lcEnvs("test")...), b, lcStep("app-v1", "test", "s-test", "Verified"))
	lcReconcile(t, &bundle.Reconciler{Client: c}, "app-v1")
	got := lcGet(t, c, "app-v1")
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, ready)
	assert.True(t, old.Equal(&ready.LastTransitionTime), "LastTransitionTime moved without a transition")
	assert.True(t, vt.Equal(got.Status.Environments[0].HealthCheckedAt), "HealthCheckedAt is kept once set")
}

// C02-bundle-24: HealthCheckedAt is when the step became Verified, not when
// the Bundle reconciler first saw it.
func TestLifecycle_HealthCheckedAtFromVerifiedCondition(t *testing.T) {
	verifiedAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	step := lcStep("app-v1", "test", "s-test", "Verified")
	step.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, Reason: "Verified",
		LastTransitionTime: metav1.NewTime(verifiedAt)}}
	c := lcClient(lcPipeline("app", lcEnvs("test", "prod")...),
		lcBundle("app-v1", "image", "Promoting", time.Now().UTC().Add(-3*time.Hour)), step)
	lcReconcile(t, &bundle.Reconciler{Client: c}, "app-v1")
	env := lcGet(t, c, "app-v1").Status.Environments[0]
	require.NotNil(t, env.HealthCheckedAt)
	assert.True(t, env.HealthCheckedAt.Time.Equal(verifiedAt), "got %s, want %s", env.HealthCheckedAt.Time, verifiedAt)
	assert.GreaterOrEqual(t, env.SoakMinutes, int64(119))
}

// --- concurrency and supersession ---

// C02-bundle-03: a Bundle that finished frees its maxConcurrentPromotions
// slot, whatever the type of the waiting Bundle.
func TestLifecycle_FinishedBundleFreesCapSlot(t *testing.T) {
	for _, typ := range []string{"config", "image"} {
		t.Run(typ, func(t *testing.T) {
			p := lcPipeline("app", lcEnvs("test")...)
			p.Spec.MaxConcurrentPromotions = 1
			t0 := time.Now().UTC().Add(-time.Hour)
			c := lcClient(p, lcBundle("app-v1", "image", "Promoting", t0),
				lcBundle("app-v2", typ, "Available", t0.Add(30*time.Minute)),
				lcStep("app-v1", "test", "s-test", "Verified"))
			r := &bundle.Reconciler{Client: c, Translator: &countingTranslator{}}

			// While v1 promotes, v2 waits with a reason.
			res := lcReconcile(t, r, "app-v2")
			got := lcGet(t, c, "app-v2")
			if typ == "config" {
				assert.Equal(t, "Available", got.Status.Phase)
				ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
				require.NotNil(t, ready)
				assert.Equal(t, "WaitingForSlot", ready.Reason)
				assert.Equal(t, 30*time.Second, res.RequeueAfter)
			}

			lcReconcile(t, r, "app-v1")
			v1 := lcGet(t, c, "app-v1")
			if typ == "config" {
				require.Equal(t, "Verified", v1.Status.Phase)
				// v1 leaving Promoting re-queues v2.
				reqs := r.WaitingSiblings(context.Background(), &v1)
				assert.Equal(t, []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "app-v2", Namespace: "default"}}}, reqs)
			}
			lcReconcile(t, r, "app-v2")
			assert.Equal(t, "Promoting", lcGet(t, c, "app-v2").Status.Phase)
		})
	}
}

// C02-bundle-03: when the Bundle list for the cap cannot be read, the Bundle
// waits and the error is retried; it does not start promoting past the cap.
func TestLifecycle_CapListErrorDoesNotSkipCap(t *testing.T) {
	p := lcPipeline("app", lcEnvs("test")...)
	p.Spec.MaxConcurrentPromotions = 1
	t0 := time.Now().UTC().Add(-time.Hour)
	c := indexedBuilder(newScheme()).
		WithObjects(p, lcBundle("app-v1", "config", "Promoting", t0),
			lcBundle("app-v2", "config", "Available", t0.Add(30*time.Minute))).
		WithStatusSubresource(&kardinalv1alpha1.Bundle{}, &kardinalv1alpha1.Pipeline{}, &kardinalv1alpha1.PromotionStep{}).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*kardinalv1alpha1.BundleList); ok {
					return apierrors.NewServiceUnavailable("etcd leader change")
				}
				return cl.List(ctx, list, opts...)
			},
		}).Build()
	tr := &countingTranslator{}
	r := &bundle.Reconciler{Client: c, Translator: tr}
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "app-v2", Namespace: "default"},
	})
	require.Error(t, err, "a failed read is retried, not treated as a free slot")
	assert.Equal(t, 0, tr.calls, "no Graph is created past the cap")
	assert.Equal(t, "Available", lcGet(t, c, "app-v2").Status.Phase)
}

// C02-bundle-04: within the same second the created-at annotation, not the
// name, decides which Bundle is newer.
func TestLifecycle_SameSecondSupersessionUsesCreatedAt(t *testing.T) {
	// Names produced by the bundle API for two requests 1ms apart.
	apiName := func(ts time.Time) string {
		return fmt.Sprintf("%s-%s-%d", "app", ts.Format("20060102150405"), ts.UnixNano()%10000)
	}
	first := time.Date(2026, 9, 29, 12, 0, 0, 9999, time.UTC) // suffix 9999
	second := first.Add(time.Millisecond).Add(-9999 + 12)     // suffix 12
	olderName, newerName := apiName(first), apiName(second)
	require.Less(t, newerName, olderName, "precondition: the newer name sorts first")

	older := lcBundle(olderName, "image", "Available", first.Truncate(time.Second))
	lifecycle.StampCreatedAt(older, first)
	newer := lcBundle(newerName, "image", "Available", first.Truncate(time.Second))
	lifecycle.StampCreatedAt(newer, second)
	c := lcClient(lcPipeline("app", lcEnvs("test")...), older, newer)
	r := &bundle.Reconciler{Client: c, Translator: &countingTranslator{}}
	lcReconcile(t, r, newerName)
	lcReconcile(t, r, olderName)
	assert.Equal(t, "Promoting", lcGet(t, c, newerName).Status.Phase)
	assert.Equal(t, "Superseded", lcGet(t, c, olderName).Status.Phase)
}

// --- Graph sync ---

// C02-bundle-10: a failed Graph re-translate is visible as a condition and
// evidence keeps syncing.
func TestLifecycle_GraphSyncErrorKeepsEvidence(t *testing.T) {
	b := lcBundle("app-v1", "image", "Promoting", time.Now().UTC())
	b.Status.GraphRef = "app-app-v1"
	b.Status.PipelineSpecHash = "hash-of-an-older-spec"
	c := lcClient(lcPipeline("app", lcEnvs("test", "prod")...), b, lcStep("app-v1", "test", "s-test", "Verified"))
	tr := &mockTranslator{err: errors.New("translator.Translate: graph identity: forbidden")}
	r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: existsChecker{}}
	res := lcReconcile(t, r, "app-v1")
	got := lcGet(t, c, "app-v1")
	require.NotEmpty(t, got.Status.Environments)
	assert.Equal(t, "Verified", got.Status.Environments[0].Phase)
	synced := meta.FindStatusCondition(got.Status.Conditions, "GraphSynced")
	require.NotNil(t, synced)
	assert.Equal(t, metav1.ConditionFalse, synced.Status)
	assert.Contains(t, synced.Message, "forbidden")
	assert.Equal(t, 30*time.Second, res.RequeueAfter)
	assert.Equal(t, "hash-of-an-older-spec", got.Status.PipelineSpecHash, "the hash moves only after a successful update")

	tr.err = nil
	lcReconcile(t, r, "app-v1")
	got = lcGet(t, c, "app-v1")
	synced = meta.FindStatusCondition(got.Status.Conditions, "GraphSynced")
	require.NotNil(t, synced)
	assert.Equal(t, metav1.ConditionTrue, synced.Status)
	assert.NotEqual(t, "hash-of-an-older-spec", got.Status.PipelineSpecHash)
}

// Pausing a Pipeline does not re-translate every in-flight Graph.
func TestLifecycle_PauseDoesNotChangePipelineSpecHash(t *testing.T) {
	p := lcPipeline("app", lcEnvs("test")...)
	b := lcBundle("app-v1", "image", "Available", time.Now().UTC())
	c := lcClient(p, b)
	tr := &countingTranslator{}
	r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: existsChecker{}}
	lcReconcile(t, r, "app-v1")
	require.Equal(t, 1, tr.calls)
	hash := lcGet(t, c, "app-v1").Status.PipelineSpecHash
	require.NotEmpty(t, hash)

	var pl kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "app", Namespace: "default"}, &pl))
	pl.Spec.Paused = true
	require.NoError(t, c.Update(context.Background(), &pl))
	lcReconcile(t, r, "app-v1")
	assert.Equal(t, 1, tr.calls, "pause must not re-translate the Graph")
	assert.Equal(t, hash, lcGet(t, c, "app-v1").Status.PipelineSpecHash)
}

// C13b-design-07: the Graph's Accepted and Ready conditions are mirrored, and
// a Graph kro rejects fails the Bundle.
func TestLifecycle_GraphConditionsMirrored(t *testing.T) {
	newGraph := func(gen int64, conds ...metav1.Condition) *graph.Graph {
		g := &graph.Graph{ObjectMeta: metav1.ObjectMeta{Name: "app-app-v1", Namespace: "default", Generation: gen}}
		g.Status.Conditions = conds
		return g
	}
	tests := []struct {
		name          string
		g             *graph.Graph
		wantPhase     string
		wantAccepted  metav1.ConditionStatus
		wantReady     metav1.ConditionStatus
		wantFailedMsg string
	}{
		{
			name: "accepted and converging",
			g: newGraph(1,
				metav1.Condition{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Compiled", ObservedGeneration: 1},
				metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "ResourcesNotReady", ObservedGeneration: 1}),
			wantPhase: "Promoting", wantAccepted: metav1.ConditionTrue, wantReady: metav1.ConditionFalse,
		},
		{
			name: "rejected by kro",
			g: newGraph(1,
				metav1.Condition{Type: "Accepted", Status: metav1.ConditionFalse, Reason: "InvalidGraph",
					Message: "node id \"status\" is reserved", ObservedGeneration: 1}),
			wantPhase: "Failed", wantAccepted: metav1.ConditionFalse, wantFailedMsg: "node id \"status\" is reserved",
		},
		{
			name: "a rejection of an older generation is ignored",
			g: newGraph(2,
				metav1.Condition{Type: "Accepted", Status: metav1.ConditionFalse, Reason: "InvalidGraph", ObservedGeneration: 1}),
			wantPhase: "Promoting",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := lcBundle("app-v1", "image", "Promoting", time.Now().UTC())
			b.Status.GraphRef = "app-app-v1"
			c := lcClient(lcPipeline("app", lcEnvs("test")...), b)
			tr := &countingTranslator{}
			r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: &graphStatusReader{g: tc.g}}
			lcReconcile(t, r, "app-v1")
			got := lcGet(t, c, "app-v1")
			assert.Equal(t, tc.wantPhase, got.Status.Phase)
			assert.Zero(t, tr.calls, "an existing Graph is not recreated")
			acc := meta.FindStatusCondition(got.Status.Conditions, "GraphAccepted")
			if tc.wantAccepted == "" {
				assert.Nil(t, acc)
			} else {
				require.NotNil(t, acc)
				assert.Equal(t, tc.wantAccepted, acc.Status)
			}
			if tc.wantReady != "" {
				ready := meta.FindStatusCondition(got.Status.Conditions, "GraphReady")
				require.NotNil(t, ready)
				assert.Equal(t, tc.wantReady, ready.Status)
			}
			if tc.wantFailedMsg != "" {
				failed := meta.FindStatusCondition(got.Status.Conditions, "Failed")
				require.NotNil(t, failed)
				assert.Equal(t, "GraphRejected", failed.Reason)
				assert.Contains(t, failed.Message, tc.wantFailedMsg)
			}
		})
	}
}

// E2E-R03: kro re-evaluates readyWhen on a backoff requeue, so the Graph
// turns Ready some time after the last PromotionStep is Verified and the Bundle
// with it. The Graph event must still refresh the Bundle's mirrored GraphReady;
// a Superseded Bundle's Graph never converges, so it is not read.
func TestLifecycle_GraphReadyMirroredAfterVerified(t *testing.T) {
	readyGraph := &graph.Graph{ObjectMeta: metav1.ObjectMeta{Name: "app-app-v1", Namespace: "default", Generation: 1}}
	readyGraph.Status.Conditions = []metav1.Condition{
		{Type: "Accepted", Status: metav1.ConditionTrue, Reason: "Compiled", ObservedGeneration: 1},
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 1},
	}
	tests := []struct {
		name       string
		phase      string
		mirrored   metav1.ConditionStatus
		wantReady  metav1.ConditionStatus
		wantReason string
		wantGets   int
	}{
		{name: "verified bundle picks up the converged graph", phase: "Verified",
			mirrored: metav1.ConditionFalse, wantReady: metav1.ConditionTrue, wantReason: "Ready", wantGets: 1},
		{name: "an already ready mirror is not re-read", phase: "Verified",
			mirrored: metav1.ConditionTrue, wantReady: metav1.ConditionTrue, wantReason: "Ready", wantGets: 0},
		{name: "superseded bundle is left alone", phase: "Superseded",
			mirrored: metav1.ConditionFalse, wantReady: metav1.ConditionFalse, wantReason: "WaitingForReadiness", wantGets: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := lcBundle("app-v1", "image", tc.phase, time.Now().UTC())
			b.Status.GraphRef = "app-app-v1"
			reason := "WaitingForReadiness"
			if tc.mirrored == metav1.ConditionTrue {
				reason = "Ready"
			}
			meta.SetStatusCondition(&b.Status.Conditions, metav1.Condition{
				Type: "GraphReady", Status: tc.mirrored, Reason: reason, Message: `node "prod" readyWhen is false`})
			c := lcClient(lcPipeline("app", lcEnvs("test")...), b, lcStep("app-v1", "test", "s-test", "Verified"))
			tr := &countingTranslator{}
			reader := &graphStatusReader{g: readyGraph}
			r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: reader}
			lcReconcile(t, r, "app-v1")
			got := lcGet(t, c, "app-v1")
			assert.Equal(t, tc.phase, got.Status.Phase)
			assert.Zero(t, tr.calls, "a terminal Bundle's Graph is never re-translated")
			assert.Equal(t, tc.wantGets, reader.gets)
			ready := meta.FindStatusCondition(got.Status.Conditions, "GraphReady")
			require.NotNil(t, ready)
			assert.Equal(t, tc.wantReady, ready.Status)
			assert.Equal(t, tc.wantReason, ready.Reason)
		})
	}
}

// #490: a Graph deleted externally is recreated.
func TestLifecycle_MissingGraphRecreated(t *testing.T) {
	b := lcBundle("app-v1", "image", "Promoting", time.Now().UTC())
	b.Status.GraphRef = "app-app-v1"
	c := lcClient(lcPipeline("app", lcEnvs("test")...), b)
	tr := &countingTranslator{}
	r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: &graphStatusReader{}}
	lcReconcile(t, r, "app-v1")
	assert.Equal(t, 1, tr.calls)
}

// graphCreatingTranslator records Translate calls and creates the Graph the
// reader then finds, like the real translator.
type graphCreatingTranslator struct {
	reader *graphStatusReader
	calls  int
}

func (m *graphCreatingTranslator) Translate(_ context.Context, _ *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) (string, error) {
	m.calls++
	name := "app-" + b.Name
	m.reader.g = &graph.Graph{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.Namespace, Generation: 1}}
	return name, nil
}

// Review item 5: a Failed Bundle is still active, so deleting its Graph used
// to recreate the Graph (#490) and, with the steps gone, count as "nothing is
// failing any more": the Bundle went back to Promoting and promoted the failed
// image again from the first environment. The Graph of a Bundle that failed
// promoting is no longer recreated and the Bundle stays Failed with
// GraphSynced=False/GraphDeleted; a Pipeline change is the explicit retry.
func TestLifecycle_FailedBundleGraphDeletedIsNotRecreated(t *testing.T) {
	ctx := context.Background()
	b := lcBundle("app-v1", "image", "Failed", time.Now().UTC())
	b.Status.GraphRef = "app-app-v1"
	b.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Failed", Message: "promotion failed", LastTransitionTime: metav1.Now()},
		{Type: "Failed", Status: metav1.ConditionTrue, Reason: "StepFailed", Message: "environment test: health check failed", LastTransitionTime: metav1.Now()},
	}
	// The steps went with the Graph (owner references), so there are none.
	c := lcClient(lcPipeline("app", lcEnvs("test", "prod")...), b)
	reader := &graphStatusReader{}
	tr := &graphCreatingTranslator{reader: reader}
	r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: reader}

	res := lcReconcile(t, r, "app-v1")
	assert.Zero(t, res.RequeueAfter, "a deleted Graph is not a transient error to retry")
	got := lcGet(t, c, "app-v1")
	assert.Zero(t, tr.calls, "the Graph of a failed Bundle is not recreated")
	assert.Equal(t, "Failed", got.Status.Phase, "missing steps are not a recovery")
	synced := meta.FindStatusCondition(got.Status.Conditions, "GraphSynced")
	require.NotNil(t, synced)
	assert.Equal(t, metav1.ConditionFalse, synced.Status)
	assert.Equal(t, "GraphDeleted", synced.Reason)
	assert.Contains(t, synced.Message, "change the Pipeline")
	assert.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, "Failed"))

	rv := got.ResourceVersion
	lcReconcile(t, r, "app-v1")
	got = lcGet(t, c, "app-v1")
	assert.Zero(t, tr.calls)
	assert.Equal(t, "Failed", got.Status.Phase)
	assert.Equal(t, rv, got.ResourceVersion, "a second reconcile writes nothing")

	// The explicit retry: a Pipeline change rebuilds the Graph in place.
	var pl kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "app", Namespace: "default"}, &pl))
	pl.Spec.Environments = lcEnvs("test", "uat", "prod")
	require.NoError(t, c.Update(ctx, &pl))
	lcReconcile(t, r, "app-v1")
	got = lcGet(t, c, "app-v1")
	assert.Equal(t, 1, tr.calls, "a Pipeline change rebuilds the Graph")
	assert.Equal(t, "Failed", got.Status.Phase, "it recovers once kro has created the new steps")
	assert.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, "GraphSynced"))

	require.NoError(t, c.Create(ctx, lcStep("app-v1", "test", "s-test", "Pending")))
	lcReconcile(t, r, "app-v1")
	got = lcGet(t, c, "app-v1")
	assert.Equal(t, "Promoting", got.Status.Phase)
	assert.Equal(t, 1, tr.calls)
}

// A Bundle that only failed to build its Graph (InvalidSpec, no step failed)
// still has its deleted Graph recreated: that is how it retries.
func TestLifecycle_InvalidSpecBundleGraphStillRecreated(t *testing.T) {
	b := lcBundle("app-v1", "image", "Failed", time.Now().UTC())
	b.Status.GraphRef = "app-app-v1"
	b.Status.Conditions = []metav1.Condition{
		{Type: "InvalidSpec", Status: metav1.ConditionTrue, Reason: "GraphBuildFailed", Message: "skip denied", LastTransitionTime: metav1.Now()},
	}
	c := lcClient(lcPipeline("app", lcEnvs("test")...), b)
	reader := &graphStatusReader{}
	tr := &graphCreatingTranslator{reader: reader}
	r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: reader}
	lcReconcile(t, r, "app-v1")
	assert.Equal(t, 1, tr.calls)
	got := lcGet(t, c, "app-v1")
	assert.Equal(t, "Promoting", got.Status.Phase)
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, "InvalidSpec"))
}

// --- missing Pipeline ---

// C02-bundle-11: a Bundle whose Pipeline does not exist is kept and says so.
func TestLifecycle_MissingPipelineKeepsBundle(t *testing.T) {
	b := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "ap-v1", Namespace: "default", CreationTimestamp: metav1.Now()},
		Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "ap"},
	}
	c := lcClient(lcPipeline("app", lcEnvs("test")...), b)
	r := &bundle.Reconciler{Client: c, Translator: &countingTranslator{}}
	lcReconcile(t, r, "ap-v1")
	res := lcReconcile(t, r, "ap-v1")
	got := lcGet(t, c, "ap-v1")
	assert.Equal(t, "Available", got.Status.Phase)
	ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
	require.NotNil(t, ready)
	assert.Equal(t, "PipelineNotFound", ready.Reason)
	assert.Equal(t, 30*time.Second, res.RequeueAfter)

	// Creating the Pipeline starts the Bundle.
	require.NoError(t, c.Create(context.Background(), lcPipeline("ap", lcEnvs("test")...)))
	lcReconcile(t, r, "ap-v1")
	assert.Equal(t, "Promoting", lcGet(t, c, "ap-v1").Status.Phase)
}

// C02-bundle-23: without a Pipeline, neither an Available nor an unbound
// Promoting Bundle is deleted.
func TestLifecycle_UnboundBundleWithoutPipelineNotDeleted(t *testing.T) {
	for _, phase := range []string{"Available", "Promoting"} {
		t.Run(phase, func(t *testing.T) {
			c := lcClient(lcBundle("app-v1", "image", phase, time.Now().UTC()))
			lcReconcile(t, &bundle.Reconciler{Client: c, Translator: &mockTranslator{graphName: "graph"}}, "app-v1")
			assert.Equal(t, phase, lcGet(t, c, "app-v1").Status.Phase)
		})
	}
}

// --- watches (C02-bundle-14) ---

func TestLifecycle_WatchMappers(t *testing.T) {
	t0 := time.Now().UTC()
	objs := []client.Object{
		lcBundle("app-new", "image", "", t0),
		lcBundle("app-avail", "image", "Available", t0),
		lcBundle("app-prom", "config", "Promoting", t0),
		lcBundle("app-ver", "image", "Verified", t0),
		lcBundle("app-fail", "image", "Failed", t0),
		lcBundle("app-sup", "image", "Superseded", t0),
	}
	other := lcBundle("other-v1", "image", "Available", t0)
	other.Spec.Pipeline = "other"
	c := lcClient(append(objs, other)...)
	r := &bundle.Reconciler{Client: c}

	names := func(reqs []reconcile.Request) []string {
		out := make([]string, 0, len(reqs))
		for _, q := range reqs {
			out = append(out, q.Name)
		}
		return out
	}

	self := lcGet(t, c, "app-ver")
	assert.ElementsMatch(t, []string{"app-new", "app-avail", "app-prom"}, names(r.WaitingSiblings(context.Background(), &self)),
		"a phase change re-queues the siblings that are waiting or in flight")
	assert.ElementsMatch(t, []string{"app-new", "app-avail", "app-prom", "app-ver", "app-fail", "app-sup"},
		names(r.PipelineBundles(context.Background(), lcPipeline("app"))),
		"a Pipeline change re-queues all its Bundles: failed ones retry, finished ones self-delete when the Pipeline is gone")

	ps := lcStep("app-prom", "test", "s", "Promoting")
	assert.Equal(t, []string{"app-prom"}, names(bundle.BundleLabelMapper(context.Background(), ps)))
	assert.Empty(t, bundle.BundleLabelMapper(context.Background(), &kardinalv1alpha1.PromotionStep{}))

	assert.Equal(t, []string{"app"}, bundle.BundlePipelineIndex(lcBundle("x", "image", "", t0)))
	assert.Nil(t, bundle.BundlePipelineIndex(&kardinalv1alpha1.Pipeline{}))

	oldB, newB := lcBundle("x", "image", "Promoting", t0), lcBundle("x", "image", "Promoting", t0)
	assert.False(t, bundle.BundlePhaseChanged.Update(event.UpdateEvent{ObjectOld: oldB, ObjectNew: newB}),
		"a status write that keeps the phase does not fan out")
	newB.Status.Phase = "Verified"
	assert.True(t, bundle.BundlePhaseChanged.Update(event.UpdateEvent{ObjectOld: oldB, ObjectNew: newB}))
	assert.True(t, bundle.BundlePhaseChanged.Create(event.CreateEvent{Object: newB}),
		"a new Bundle re-queues its siblings so older ones are superseded")
}
