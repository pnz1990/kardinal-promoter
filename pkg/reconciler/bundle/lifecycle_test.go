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
	g *graph.Graph
}

func (m *graphStatusReader) GraphExists(_ context.Context, _, _ string) (bool, error) {
	return m.g != nil, nil
}

func (m *graphStatusReader) Get(_ context.Context, _, name string) (*graph.Graph, error) {
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
// Pipeline is corrected.
func TestLifecycle_InvalidPipelineRetriedWhenCorrected(t *testing.T) {
	p := lcPipeline("app",
		kardinalv1alpha1.EnvironmentSpec{Name: "a", DependsOn: []string{"b"}},
		kardinalv1alpha1.EnvironmentSpec{Name: "b", DependsOn: []string{"a"}})
	c := lcClient(p, lcBundle("app-v1", "image", "Available", time.Now().UTC()))
	tr := &countingTranslator{}
	r := &bundle.Reconciler{Client: c, Translator: tr}
	lcReconcile(t, r, "app-v1")

	got := lcGet(t, c, "app-v1")
	assert.Equal(t, "Failed", got.Status.Phase)
	inv := meta.FindStatusCondition(got.Status.Conditions, "InvalidSpec")
	require.NotNil(t, inv)
	assert.Equal(t, "CircularDependency", inv.Reason)
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
