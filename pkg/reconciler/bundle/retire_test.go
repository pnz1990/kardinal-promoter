// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

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
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
)

// deletingChecker is a GraphChecker that records Graph deletes.
type deletingChecker struct {
	exists  bool
	deleted []string
}

func (d *deletingChecker) GraphExists(context.Context, string, string) (bool, error) {
	return d.exists, nil
}

func (d *deletingChecker) Delete(_ context.Context, ns, name string) error {
	if !d.exists {
		return apierrors.NewNotFound(schema.GroupResource{Group: "kro.run", Resource: "graphs"}, name)
	}
	d.exists = false
	d.deleted = append(d.deleted, ns+"/"+name)
	return nil
}

// lcRetire runs the Bundle reconciler, then the retirement controller, on
// Bundle name, as both controllers would on an event, and returns the
// retirement controller's result.
func lcRetire(t *testing.T, r *bundle.Reconciler, name string) ctrl.Result {
	t.Helper()
	lcReconcile(t, r, name)
	res, err := r.ReconcileRetire(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}})
	require.NoError(t, err)
	return res
}

var testRetire = bundle.RetirePolicy{Superseded: 10 * time.Minute, Failed: time.Hour, Verified: 30 * time.Minute}

// backdateRetire moves the start of b's retirement delay back by d.
func backdateRetire(t *testing.T, c client.Client, name string, d time.Duration) {
	t.Helper()
	b := lcGet(t, c, name)
	for i := range b.Status.Conditions {
		if b.Status.Conditions[i].Type == lifecycle.ConditionGraphRetired {
			b.Status.Conditions[i].LastTransitionTime = metav1.NewTime(b.Status.Conditions[i].LastTransitionTime.Add(-d))
		}
	}
	require.NoError(t, c.Status().Update(context.Background(), &b))
}

func retireCond(t *testing.T, c client.Client, name string) *metav1.Condition {
	t.Helper()
	b := lcGet(t, c, name)
	return meta.FindStatusCondition(b.Status.Conditions, lifecycle.ConditionGraphRetired)
}

// TestRetire_SupersededBundle walks a Superseded Bundle through retirement
// (#1492): a step still closing its PR holds it and the delay does not run,
// the delay starts once every step has settled, nothing happens before it
// passes, then the steps are recorded in
// status.retiredSteps, the Graph is deleted once, and later reconciles
// change nothing.
func TestRetire_SupersededBundle(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC().Add(-2 * time.Hour)
	v1 := lcBundle("app-v1", "image", "Superseded", t0)
	v1.Status.GraphRef = "app-app-v1"
	v1.Status.Environments = []kardinalv1alpha1.EnvironmentStatus{{Name: "test", Phase: "Verified"}}
	verified := lcStep("app-v1", "test", "s-test", "Verified")
	verified.CreationTimestamp = metav1.NewTime(t0.Truncate(time.Second))
	verified.Status.PRURL = "https://git.example/pr/1"
	verified.Status.HealthCheckExpiry = &metav1.Time{Time: t0.Add(time.Minute).Truncate(time.Second)}
	verified.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue, Reason: "Verified",
		LastTransitionTime: metav1.NewTime(t0.Add(2 * time.Minute).Truncate(time.Second))}}
	closing := lcStep("app-v1", "prod", "s-prod", "Promoting")
	closing.Finalizers = []string{"kardinal.io/close-pr"}
	c := lcClient(lcPipeline("app", lcEnvs("test", "prod")...), v1, verified, closing,
		lcBundle("app-v2", "image", "Promoting", t0.Add(time.Minute)))
	checker := &deletingChecker{exists: true}
	r := &bundle.Reconciler{Client: c, GraphChecker: checker, Retire: testRetire}

	// A step still holding its PR finalizer: the delay has not started.
	res := lcRetire(t, r, "app-v1")
	cond := retireCond(t, c, "app-v1")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, "WaitingForSteps", cond.Reason)
	assert.Contains(t, cond.Message, "s-prod")
	assert.Equal(t, time.Minute, res.RequeueAfter)
	backdateRetire(t, c, "app-v1", time.Hour)
	lcRetire(t, r, "app-v1")
	assert.Empty(t, checker.deleted, "time spent waiting for steps does not count")

	// The step settles: the delay starts.
	var ps kardinalv1alpha1.PromotionStep
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(closing), &ps))
	ps.Finalizers = nil
	require.NoError(t, c.Update(ctx, &ps))
	lcSetStepState(t, c, "s-prod", "Failed")
	res = lcRetire(t, r, "app-v1")
	cond = retireCond(t, c, "app-v1")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "Scheduled", cond.Reason)
	assert.Equal(t, 10*time.Minute, res.RequeueAfter)

	// Before the delay: nothing.
	res = lcRetire(t, r, "app-v1")
	assert.Positive(t, res.RequeueAfter)
	assert.Empty(t, checker.deleted)

	backdateRetire(t, c, "app-v1", 11*time.Minute)
	lcRetire(t, r, "app-v1")

	got := lcGet(t, c, "app-v1")
	assert.True(t, lifecycle.Retired(&got))
	cond = meta.FindStatusCondition(got.Status.Conditions, lifecycle.ConditionGraphRetired)
	assert.Equal(t, "Retired", cond.Reason)
	assert.Equal(t, []string{"default/app-app-v1"}, checker.deleted)
	require.Len(t, got.Status.RetiredSteps, 2)
	rec := got.Status.RetiredSteps[1]
	assert.Equal(t, "s-test", rec.Name)
	assert.Equal(t, "test", rec.Environment)
	assert.Equal(t, "Verified", rec.State)
	assert.Equal(t, "https://git.example/pr/1", rec.PRURL)
	require.NotNil(t, rec.VerifiedAt)
	assert.True(t, rec.VerifiedAt.Equal(&verified.Status.Conditions[0].LastTransitionTime))
	assert.NotNil(t, rec.HealthCheckExpiry)
	assert.Equal(t, "Failed", got.Status.RetiredSteps[0].State)

	// Idempotent: nothing more is written or deleted, even once kro has
	// deleted the steps.
	require.NoError(t, c.Delete(ctx, &ps))
	rv := got.ResourceVersion
	lcRetire(t, r, "app-v1")
	assert.Equal(t, rv, lcGet(t, c, "app-v1").ResourceVersion)
	assert.Len(t, checker.deleted, 1)
}

// TestRetire_Delays checks which delay applies: Superseded, Failed, Verified
// still deployed, Verified replaced in every environment, the Pipeline
// annotation, and a zero delay that keeps the Graph.
func TestRetire_Delays(t *testing.T) {
	t0 := time.Now().UTC().Add(-2 * time.Hour)
	verifiedIn := func(b *kardinalv1alpha1.Bundle, envs ...string) *kardinalv1alpha1.Bundle {
		for _, e := range envs {
			b.Status.Environments = append(b.Status.Environments, kardinalv1alpha1.EnvironmentStatus{Name: e, Phase: "Verified"})
		}
		return b
	}
	tests := []struct {
		name       string
		phase      string
		newer      *kardinalv1alpha1.Bundle
		annotation string
		policy     bundle.RetirePolicy
		want       time.Duration // 0: no GraphRetired condition
	}{
		{name: "superseded", phase: "Superseded", policy: testRetire, want: 10 * time.Minute},
		{name: "failed", phase: "Failed", policy: testRetire, want: time.Hour},
		{name: "verified, deployed", phase: "Verified", policy: testRetire, want: 30 * time.Minute},
		{name: "verified, replaced in one environment only", phase: "Verified", policy: testRetire,
			newer: verifiedIn(lcBundle("app-v2", "image", "Promoting", t0.Add(time.Minute)), "test"), want: 30 * time.Minute},
		{name: "verified, replaced everywhere", phase: "Verified", policy: testRetire,
			newer: verifiedIn(lcBundle("app-v2", "image", "Verified", t0.Add(time.Minute)), "test", "prod"), want: 10 * time.Minute},
		{name: "verified, replaced by another type", phase: "Verified", policy: testRetire,
			newer: verifiedIn(lcBundle("app-v2", "config", "Verified", t0.Add(time.Minute)), "test", "prod"), want: 30 * time.Minute},
		{name: "annotation", phase: "Verified", policy: testRetire, annotation: "2m", want: 2 * time.Minute},
		{name: "annotation keeps the Graph", phase: "Superseded", policy: testRetire, annotation: "0"},
		{name: "invalid annotation is ignored", phase: "Superseded", policy: testRetire, annotation: "soon", want: 10 * time.Minute},
		{name: "zero delay keeps the Graph", phase: "Failed", policy: bundle.RetirePolicy{Superseded: time.Minute}},
		{name: "promoting", phase: "Promoting", policy: testRetire},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v1 := verifiedIn(lcBundle("app-v1", "image", tc.phase, t0), "test", "prod")
			v1.Status.GraphRef = "app-app-v1"
			p := lcPipeline("app", lcEnvs("test", "prod")...)
			if tc.annotation != "" {
				p.Annotations = map[string]string{bundle.AnnotationGraphRetireAfter: tc.annotation}
			}
			prodState := map[string]string{"Failed": "Failed", "Promoting": "Promoting"}[tc.phase]
			if prodState == "" {
				prodState = "Verified"
			}
			objs := []client.Object{p, v1,
				lcStep("app-v1", "test", "s-test", "Verified"), lcStep("app-v1", "prod", "s-prod", prodState)}
			if tc.newer != nil {
				objs = append(objs, tc.newer)
			}
			c := lcClient(objs...)
			res := lcRetire(t, &bundle.Reconciler{Client: c, GraphChecker: &deletingChecker{exists: true},
				Retire: tc.policy}, "app-v1")
			cond := retireCond(t, c, "app-v1")
			if tc.want == 0 {
				assert.Nil(t, cond)
				return
			}
			require.NotNil(t, cond)
			assert.Equal(t, "Scheduled", cond.Reason)
			assert.Equal(t, tc.want, res.RequeueAfter)
		})
	}
}

// TestRetire_RetiredFailedBundleIsFinal checks that a retired Failed Bundle
// is not rebuilt by a Pipeline change (its Graph would promote the failed
// artifacts again from the first environment), and that a Failed Bundle that
// recovers before it retires loses its schedule.
func TestRetire_RetiredFailedBundleIsFinal(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC().Add(-2 * time.Hour)
	v1 := lcBundle("app-v1", "image", "Failed", t0)
	v1.Status.GraphRef = "app-app-v1"
	v1.Status.PipelineSpecHash = "stale"
	v1.Status.Conditions = []metav1.Condition{
		{Type: "Failed", Status: metav1.ConditionTrue, Reason: "StepFailed", LastTransitionTime: metav1.NewTime(t0)},
		{Type: lifecycle.ConditionGraphRetired, Status: metav1.ConditionTrue, Reason: "Retired", LastTransitionTime: metav1.NewTime(t0)},
	}
	v1.Status.RetiredSteps = []kardinalv1alpha1.RetiredStep{{Name: "s-test", Environment: "test", State: "Failed",
		CreatedAt: metav1.NewTime(t0)}}
	v1.Status.RetiredAt = &metav1.Time{Time: t0}
	c := lcClient(lcPipeline("app", lcEnvs("test")...), v1)
	tr := &countingTranslator{}
	r := &bundle.Reconciler{Client: c, Translator: tr, GraphChecker: &deletingChecker{}, Retire: testRetire}
	rv := lcGet(t, c, "app-v1").ResourceVersion
	lcRetire(t, r, "app-v1")
	assert.Zero(t, tr.calls, "a retired Bundle is never translated again")
	assert.Equal(t, rv, lcGet(t, c, "app-v1").ResourceVersion)

	// A Failed Bundle that recovers loses its schedule.
	v2 := lcBundle("app-v2", "image", "Failed", t0.Add(time.Minute))
	v2.Status.GraphRef = "app-app-v2"
	v2.Status.Conditions = []metav1.Condition{
		{Type: "Failed", Status: metav1.ConditionTrue, Reason: "StepFailed", LastTransitionTime: metav1.NewTime(t0)},
		{Type: lifecycle.ConditionGraphRetired, Status: metav1.ConditionFalse, Reason: "Scheduled", LastTransitionTime: metav1.NewTime(t0)},
	}
	require.NoError(t, c.Create(ctx, v2))
	require.NoError(t, c.Create(ctx, lcStep("app-v2", "test", "s2-test", "Promoting")))
	lcSetStepState(t, c, "s2-test", "Promoting")
	r.Translator = nil
	lcRetire(t, r, "app-v2")
	got := lcGet(t, c, "app-v2")
	assert.Equal(t, "Promoting", got.Status.Phase)
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, lifecycle.ConditionGraphRetired))
}

// TestRetire_FinishedPhaseFilter checks the retirement controller's event
// filter: finished Bundles not yet fully retired, and a Bundle that left a
// finished phase with a GraphRetired condition to clear.
func TestRetire_FinishedPhaseFilter(t *testing.T) {
	t0 := time.Now()
	cond := func(status metav1.ConditionStatus, reason string) []metav1.Condition {
		return []metav1.Condition{{Type: lifecycle.ConditionGraphRetired, Status: status, Reason: reason}}
	}
	for _, tc := range []struct {
		name  string
		phase string
		conds []metav1.Condition
		want  bool
	}{
		{name: "superseded", phase: "Superseded", want: true},
		{name: "verified scheduled", phase: "Verified", conds: cond(metav1.ConditionFalse, "Scheduled"), want: true},
		{name: "failed retiring", phase: "Failed", conds: cond(metav1.ConditionTrue, "Retiring"), want: true},
		{name: "retired", phase: "Superseded", conds: cond(metav1.ConditionTrue, "Retired")},
		{name: "promoting", phase: "Promoting"},
		{name: "recovered with a schedule", phase: "Promoting", conds: cond(metav1.ConditionFalse, "Scheduled"), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := lcBundle("app-v1", "image", tc.phase, t0)
			b.Status.Conditions = tc.conds
			assert.Equal(t, tc.want, bundle.FinishedPhase(b))
		})
	}
}

// TestRetire_StaleCacheDoesNotRebuild checks that the Bundle reconciler,
// reading a copy of a Failed Bundle from before the retirement controller
// retired it, does not rebuild its Graph on a Pipeline change: translate reads
// the Bundle fresh from the API server first.
func TestRetire_StaleCacheDoesNotRebuild(t *testing.T) {
	t0 := time.Now().UTC().Add(-2 * time.Hour)
	stale := lcBundle("app-v1", "image", "Failed", t0)
	stale.Status.GraphRef = "app-app-v1"
	stale.Status.PipelineSpecHash = "an older spec"
	stale.Status.Conditions = []metav1.Condition{{Type: "Failed", Status: metav1.ConditionTrue, Reason: "StepFailed",
		LastTransitionTime: metav1.NewTime(t0)}}
	fresh := stale.DeepCopy()
	fresh.Status.RetiredAt = &metav1.Time{Time: t0}
	cache := lcClient(lcPipeline("app", lcEnvs("test")...), stale)
	api := lcClient(fresh)
	tr := &countingTranslator{}
	r := &bundle.Reconciler{Client: cache, APIReader: api, Translator: tr, GraphChecker: &deletingChecker{}}
	lcReconcile(t, r, "app-v1")
	assert.Zero(t, tr.calls, "a retired Bundle's Graph is not built again")
}

// TestRetire_SettledStates checks which step states let a Bundle retire:
// Verified, Failed and RollingBack settle; AbortedByAlarm settles too but is
// reported, so a Bundle that is not Superseded keeps its Graph as long as a
// Failed one; a finalizer or a running state does not settle.
func TestRetire_SettledStates(t *testing.T) {
	step := func(state string, finalizer bool) kardinalv1alpha1.PromotionStep {
		s := *lcStep("app-v1", "test", "s-"+state, state)
		if finalizer {
			s.Finalizers = []string{"kardinal.io/close-pr"}
		}
		return s
	}
	for _, tc := range []struct {
		state     string
		finalizer bool
		busy      bool
		wantAbort bool
	}{
		{state: "Verified"}, {state: "Failed"}, {state: "RollingBack"},
		{state: "AbortedByAlarm", wantAbort: true},
		{state: "HealthChecking", busy: true}, {state: "", busy: true},
		{state: "Failed", finalizer: true, busy: true},
	} {
		t.Run(fmt.Sprintf("%s finalizer=%v", tc.state, tc.finalizer), func(t *testing.T) {
			busy, aborted := bundle.UnsettledStep([]kardinalv1alpha1.PromotionStep{step(tc.state, tc.finalizer)})
			assert.Equal(t, tc.busy, busy != "", busy)
			assert.Equal(t, tc.wantAbort, aborted)
		})
	}
}

// TestRetire_AbortedByAlarmDelay checks the delay of a Bundle with a step
// stopped by a health alarm: the Failed delay unless it was superseded, and
// a RollingBack step settles.
func TestRetire_AbortedByAlarmDelay(t *testing.T) {
	t0 := time.Now().UTC().Add(-2 * time.Hour)
	for _, tc := range []struct {
		phase, state string
		want         time.Duration
	}{
		{phase: "Failed", state: "AbortedByAlarm", want: time.Hour},
		{phase: "Superseded", state: "AbortedByAlarm", want: 10 * time.Minute},
		{phase: "Superseded", state: "RollingBack", want: 10 * time.Minute},
	} {
		t.Run(tc.phase+" "+tc.state, func(t *testing.T) {
			v1 := lcBundle("app-v1", "image", tc.phase, t0)
			v1.Status.GraphRef = "app-app-v1"
			c := lcClient(lcPipeline("app", lcEnvs("test")...), v1, lcStep("app-v1", "test", "s-test", tc.state))
			res, err := (&bundle.Reconciler{Client: c, GraphChecker: &deletingChecker{exists: true}, Retire: testRetire}).
				ReconcileRetire(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}})
			require.NoError(t, err)
			cond := retireCond(t, c, "app-v1")
			require.NotNil(t, cond)
			assert.Equal(t, "Scheduled", cond.Reason, cond.Message)
			assert.Equal(t, tc.want, res.RequeueAfter)
		})
	}
}

// TestRetire_WakesOlderVerified checks that a Bundle turning Verified
// re-queues the older Verified Bundles of its Pipeline and type that are not
// retired, so one it replaced everywhere retires after the Superseded delay.
func TestRetire_WakesOlderVerified(t *testing.T) {
	t0 := time.Now().UTC().Add(-2 * time.Hour)
	older := lcBundle("app-v1", "image", "Verified", t0)
	retiredB := lcBundle("app-v0", "image", "Verified", t0.Add(-time.Hour))
	retiredB.Status.RetiredAt = &metav1.Time{Time: t0}
	cfg := lcBundle("app-c1", "config", "Verified", t0)
	newer := lcBundle("app-v3", "image", "Verified", t0.Add(2*time.Hour))
	c := lcClient(lcPipeline("app"), older, retiredB, cfg, newer)
	r := &bundle.Reconciler{Client: c}
	v2 := lcBundle("app-v2", "image", "Verified", t0.Add(time.Hour))
	var names []string
	for _, q := range r.OlderVerified(context.Background(), v2) {
		names = append(names, q.Name)
	}
	assert.Equal(t, []string{"app-v1"}, names)

	promoting := v2.DeepCopy()
	promoting.Status.Phase = "Promoting"
	assert.True(t, bundle.BecameVerified.Update(event.UpdateEvent{ObjectOld: promoting, ObjectNew: v2}))
	assert.False(t, bundle.BecameVerified.Update(event.UpdateEvent{ObjectOld: v2, ObjectNew: v2}))
}

// TestRetire_PipelineAnnotation checks the Pipeline annotation: a change
// re-queues the finished Bundles, and an invalid value is named on the
// condition and ignored.
func TestRetire_PipelineAnnotation(t *testing.T) {
	t0 := time.Now().UTC().Add(-2 * time.Hour)
	p := lcPipeline("app", lcEnvs("test")...)
	p.Annotations = map[string]string{bundle.AnnotationGraphRetireAfter: "-5m"}
	v1 := lcBundle("app-v1", "image", "Superseded", t0)
	v1.Status.GraphRef = "app-app-v1"
	c := lcClient(p, v1, lcBundle("app-v2", "image", "Promoting", t0.Add(time.Minute)),
		lcStep("app-v1", "test", "s-test", "Failed"))
	r := &bundle.Reconciler{Client: c, GraphChecker: &deletingChecker{exists: true}, Retire: testRetire}
	res, err := r.ReconcileRetire(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}})
	require.NoError(t, err)
	assert.Equal(t, 10*time.Minute, res.RequeueAfter, "the controller's delay")
	assert.Contains(t, retireCond(t, c, "app-v1").Message, `kardinal.io/graph-retire-after="-5m" is ignored`)

	var names []string
	for _, q := range r.FinishedBundles(context.Background(), p) {
		names = append(names, q.Name)
	}
	assert.Equal(t, []string{"app-v1"}, names, "the finished Bundles, not the one promoting")
	changed := p.DeepCopy()
	changed.Annotations[bundle.AnnotationGraphRetireAfter] = "1m"
	assert.True(t, bundle.RetireAnnotationChange.Update(event.UpdateEvent{ObjectOld: p, ObjectNew: changed}))
	assert.False(t, bundle.RetireAnnotationChange.Update(event.UpdateEvent{ObjectOld: p, ObjectNew: p}))
}

// TestRetire_Limits checks the size limits of status.retiredSteps, flag
// validation, and that a Graph client that cannot delete is an error.
func TestRetire_Limits(t *testing.T) {
	many := make([]kardinalv1alpha1.RetiredStep, 1001)
	assert.Contains(t, bundle.TooManySteps(many), "more than status.retiredSteps holds (1000)")
	big := make([]kardinalv1alpha1.RetiredStep, 900)
	for i := range big {
		big[i] = kardinalv1alpha1.RetiredStep{Name: fmt.Sprintf("s%04d", i), Message: strings.Repeat("x", 512),
			PRURL: strings.Repeat("u", 200)}
	}
	assert.Contains(t, bundle.TooManySteps(big), "bytes, more than 524288")
	assert.Empty(t, bundle.TooManySteps(big[:10]))

	assert.NoError(t, bundle.DefaultRetirePolicy.Validate())
	assert.Error(t, bundle.RetirePolicy{Failed: -time.Second}.Validate())

	t0 := time.Now().UTC().Add(-2 * time.Hour)
	v1 := lcBundle("app-v1", "image", "Superseded", t0)
	v1.Status.GraphRef = "app-app-v1"
	v1.Status.RetiredAt = &metav1.Time{Time: t0}
	v1.Status.Conditions = []metav1.Condition{{Type: lifecycle.ConditionGraphRetired, Status: metav1.ConditionTrue,
		Reason: "Retiring", LastTransitionTime: metav1.NewTime(t0)}}
	c := lcClient(lcPipeline("app"), v1)
	_, err := (&bundle.Reconciler{Client: c, GraphChecker: &mockGraphChecker{exists: true}, Retire: testRetire}).
		ReconcileRetire(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete Graphs")
}

// TestRetire_StaleWriteCannotUnretire is the retry-vs-retire race: the Bundle
// reconciler holds a copy of a Failed Bundle from before the retirement
// controller retired it, and that copy would recover the Bundle to
// Promoting. Its status patch carries the old resourceVersion, so it fails
// with a Conflict and the reconcile is requeued; the Bundle stays retired
// and Failed.
func TestRetire_StaleWriteCannotUnretire(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC().Add(-2 * time.Hour)
	v1 := lcBundle("app-v1", "image", "Failed", t0)
	v1.Status.GraphRef = "app-app-v1"
	v1.Status.Conditions = []metav1.Condition{{Type: "Failed", Status: metav1.ConditionTrue, Reason: "StepFailed",
		LastTransitionTime: metav1.NewTime(t0)}}
	base := lcClient(lcPipeline("app", lcEnvs("test")...), v1, lcStep("app-v1", "test", "s-test", "Verified"))
	stale := lcGet(t, base, "app-v1")
	retired := stale.DeepCopy()
	retired.Status.RetiredAt = &metav1.Time{Time: time.Now()}
	require.NoError(t, base.Status().Update(ctx, retired))

	cached := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if b, ok := obj.(*kardinalv1alpha1.Bundle); ok && key.Name == "app-v1" {
				stale.DeepCopyInto(b)
				return nil
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	})
	r := &bundle.Reconciler{Client: cached}
	res := lcReconcile(t, r, "app-v1")
	assert.Equal(t, time.Second, res.RequeueAfter, "the conflict is requeued, not an error")
	got := lcGet(t, base, "app-v1")
	assert.Equal(t, "Failed", got.Status.Phase, "a stale copy cannot move a retired Bundle back to Promoting")
	assert.NotNil(t, got.Status.RetiredAt)
}

// errDeleter is a GraphChecker whose Delete fails.
type errDeleter struct{ *deletingChecker }

func (errDeleter) Delete(context.Context, string, string) error {
	return errors.New("etcd unavailable")
}

// retireNow runs the retirement controller on app-v1.
func retireNow(t *testing.T, r *bundle.Reconciler) (ctrl.Result, error) {
	t.Helper()
	return r.ReconcileRetire(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}})
}

// TestRetire_Gaps covers the paths QA listed: too many records keep the
// Graph, an invalid annotation's Event, a Verified Bundle with a step
// stopped by a health alarm, a failed Delete, the all-zero policy with and
// without the annotation, and a Failed Bundle replaced everywhere.
func TestRetire_Gaps(t *testing.T) {
	t0 := time.Now().UTC().Add(-3 * time.Hour)
	finished := func(phase string, steps ...client.Object) (client.Client, *kardinalv1alpha1.Bundle) {
		v1 := lcBundle("app-v1", "image", phase, t0)
		v1.Status.GraphRef = "app-app-v1"
		v1.Status.Environments = []kardinalv1alpha1.EnvironmentStatus{{Name: "test", Phase: "Verified"}}
		return lcClient(append([]client.Object{lcPipeline("app", lcEnvs("test")...), v1}, steps...)...), v1
	}

	t.Run("too many records", func(t *testing.T) {
		var steps []client.Object
		for i := 0; i < 250; i++ {
			s := lcStep("app-v1", "test", fmt.Sprintf("s%03d", i), "Verified")
			s.Status.PRURL = "https://git.example/" + strings.Repeat("p", 2040)
			steps = append(steps, s)
		}
		c, _ := finished("Superseded", steps...)
		r := &bundle.Reconciler{Client: c, GraphChecker: &deletingChecker{exists: true}, Retire: testRetire}
		_, err := retireNow(t, r)
		require.NoError(t, err)
		cond := retireCond(t, c, "app-v1")
		require.NotNil(t, cond)
		assert.Equal(t, "TooManySteps", cond.Reason)
		assert.Contains(t, cond.Message, "the Graph is kept")
		assert.Nil(t, lcGet(t, c, "app-v1").Status.RetiredAt)

		// Idempotent: a second reconcile finds the same condition and
		// writes nothing (QA #1527).
		rv := lcGet(t, c, "app-v1").ResourceVersion
		_, err = retireNow(t, r)
		require.NoError(t, err)
		assert.Equal(t, rv, lcGet(t, c, "app-v1").ResourceVersion, "no second status write")
	})

	t.Run("invalid annotation event", func(t *testing.T) {
		c, _ := finished("Superseded", lcStep("app-v1", "test", "s-test", "Verified"))
		var p kardinalv1alpha1.Pipeline
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "app"}, &p))
		p.Annotations = map[string]string{bundle.AnnotationGraphRetireAfter: "soon"}
		require.NoError(t, c.Update(context.Background(), &p))
		rec := events.NewFakeRecorder(10)
		_, err := retireNow(t, &bundle.Reconciler{Client: c, GraphChecker: &deletingChecker{exists: true}, Retire: testRetire, Recorder: rec})
		require.NoError(t, err)
		select {
		case e := <-rec.Events:
			assert.Contains(t, e, "InvalidRetireDelay")
			assert.Contains(t, e, `kardinal.io/graph-retire-after="soon" is ignored`)
		default:
			t.Fatal("no InvalidRetireDelay Event")
		}
	})

	t.Run("verified with an aborted step keeps the failed delay", func(t *testing.T) {
		c, _ := finished("Verified", lcStep("app-v1", "test", "s-test", "AbortedByAlarm"))
		res, err := retireNow(t, &bundle.Reconciler{Client: c, GraphChecker: &deletingChecker{exists: true}, Retire: testRetire})
		require.NoError(t, err)
		assert.Equal(t, time.Hour, res.RequeueAfter)
	})

	t.Run("delete error", func(t *testing.T) {
		c, _ := finished("Superseded", lcStep("app-v1", "test", "s-test", "Verified"))
		r := &bundle.Reconciler{Client: c, GraphChecker: errDeleter{&deletingChecker{exists: true}}, Retire: testRetire}
		_, err := retireNow(t, r)
		require.NoError(t, err)
		backdateRetire(t, c, "app-v1", time.Hour)
		_, err = retireNow(t, r)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "etcd unavailable")
		got := lcGet(t, c, "app-v1")
		assert.NotNil(t, got.Status.RetiredAt, "the records are written before the delete")
		cond := meta.FindStatusCondition(got.Status.Conditions, lifecycle.ConditionGraphRetired)
		assert.Equal(t, "Retiring", cond.Reason, "retried until the delete succeeds")
	})

	t.Run("all-zero policy", func(t *testing.T) {
		c, _ := finished("Superseded", lcStep("app-v1", "test", "s-test", "Verified"))
		r := &bundle.Reconciler{Client: c, GraphChecker: &deletingChecker{exists: true}}
		_, err := retireNow(t, r)
		require.NoError(t, err)
		assert.Nil(t, retireCond(t, c, "app-v1"), "no annotation: kept")
		var p kardinalv1alpha1.Pipeline
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "app"}, &p))
		p.Annotations = map[string]string{bundle.AnnotationGraphRetireAfter: "2m"}
		require.NoError(t, c.Update(context.Background(), &p))
		res, err := retireNow(t, r)
		require.NoError(t, err)
		assert.Equal(t, 2*time.Minute, res.RequeueAfter, "the annotation still retires")
	})

	t.Run("failed replaced everywhere", func(t *testing.T) {
		newer := lcBundle("app-v2", "image", "Verified", t0.Add(time.Hour))
		newer.Status.Environments = []kardinalv1alpha1.EnvironmentStatus{{Name: "test", Phase: "Verified"}}
		c, _ := finished("Failed", lcStep("app-v1", "test", "s-test", "Failed"), newer)
		res, err := retireNow(t, &bundle.Reconciler{Client: c, GraphChecker: &deletingChecker{exists: true}, Retire: testRetire})
		require.NoError(t, err)
		assert.Equal(t, 10*time.Minute, res.RequeueAfter, "the Superseded delay")
	})
}

// TestRetire_TranslateRace checks the TOCTOU: a Bundle retired while its
// Graph was being rebuilt gets the new Graph deleted again, and a Graph that
// appears for a Bundle already retired is deleted by the retirement
// controller.
func TestRetire_TranslateRace(t *testing.T) {
	t0 := time.Now().UTC().Add(-2 * time.Hour)
	v1 := lcBundle("app-v1", "image", "Failed", t0)
	v1.Status.GraphRef = "app-app-v1"
	v1.Status.PipelineSpecHash = "older"
	v1.Status.Conditions = []metav1.Condition{{Type: "Failed", Status: metav1.ConditionTrue, Reason: "StepFailed",
		LastTransitionTime: metav1.NewTime(t0)}}
	c := lcClient(lcPipeline("app", lcEnvs("test")...), v1)
	checker := &deletingChecker{}
	tr := &hookTranslator{after: func() {
		b := lcGet(t, c, "app-v1")
		b.Status.RetiredAt = &metav1.Time{Time: time.Now()}
		require.NoError(t, c.Status().Update(context.Background(), &b))
		checker.exists = true // the Graph Translate wrote
	}}
	r := &bundle.Reconciler{Client: c, APIReader: c, Translator: tr, GraphChecker: checker}
	lcReconcile(t, r, "app-v1")
	assert.Equal(t, 1, tr.calls)
	assert.Equal(t, []string{"default/app-app-v1"}, checker.deleted, "the Graph written for a retired Bundle is deleted")

	// A Graph that appears later for the retired Bundle.
	b := lcGet(t, c, "app-v1")
	b.Status.Conditions = append(b.Status.Conditions, metav1.Condition{Type: lifecycle.ConditionGraphRetired,
		Status: metav1.ConditionTrue, Reason: "Retired", LastTransitionTime: metav1.NewTime(t0)})
	require.NoError(t, c.Status().Update(context.Background(), &b))
	checker.exists = true
	_, err := retireNow(t, &bundle.Reconciler{Client: c, GraphChecker: checker, Retire: testRetire})
	require.NoError(t, err)
	assert.Len(t, checker.deleted, 2)
}

// hookTranslator counts calls and runs after once Translate has "written"
// the Graph.
type hookTranslator struct {
	calls int
	after func()
}

func (h *hookTranslator) Translate(_ context.Context, _ *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) (string, error) {
	h.calls++
	if h.after != nil {
		h.after()
	}
	return "app-" + b.Name, nil
}
