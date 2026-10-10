// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

func holdAudits(t *testing.T, c client.Client) []kardinalv1alpha1.AuditEvent {
	t.Helper()
	var list kardinalv1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("default")))
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list.Items
}

// TestPipelineHolds (#1528 QA): the Pipeline reconciler writes a HoldCreated
// AuditEvent for a hold added by any client and a HoldReleased one when it
// goes, removes a hold at its expiresAt (HoldReleased says so) and requeues
// for the next expiry; a reconcile that runs again writes nothing new.
func TestPipelineHolds(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	now := t0
	at := metav1.NewTime(t0)
	exp := metav1.NewTime(t0.Add(time.Hour))
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{
		{Environment: "prod", Bundle: "app-rollback-1", Reason: "INC-42", CreatedBy: "alice", CreatedAt: &at, ExpiresAt: &exp},
	}
	c := newClientWithIndex(newPipelineScheme(), p, holdBundle("app-rollback-1"), holdBundle("app-rollback-2"))
	r := &pipeline.Reconciler{Client: c, Now: func() time.Time { return now }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "default"}}
	get := func() *kardinalv1alpha1.Pipeline {
		var got kardinalv1alpha1.Pipeline
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
		return &got
	}

	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, res.RequeueAfter, "back at the expiry")
	ae := holdAudits(t, c)
	require.Len(t, ae, 1)
	assert.Equal(t, "HoldCreated", ae[0].Spec.Action)
	assert.Equal(t, "app-rollback-1", ae[0].Spec.BundleName)
	assert.Equal(t, "prod", ae[0].Spec.Environment)
	assert.Equal(t, "alice held prod on rollback app-rollback-1: INC-42 (expires 2026-10-09T11:00:00Z)", ae[0].Spec.Message)
	assert.Len(t, get().Status.ObservedHolds, 1)

	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Len(t, holdAudits(t, c), 1, "idempotent")

	// Expired: removed from the spec, then recorded as released.
	now = t0.Add(time.Hour)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, get().Spec.Holds, "the expired hold is removed")
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	ae = holdAudits(t, c)
	require.Len(t, ae, 2)
	assert.Equal(t, "HoldReleased", ae[1].Spec.Action)
	assert.Contains(t, ae[1].Spec.Message, "expired at 2026-10-09T11:00:00Z, removed by the controller")
	assert.Empty(t, get().Status.ObservedHolds)

	// Another hold, added and removed as kubectl would: both recorded.
	q := get()
	at2 := metav1.NewTime(now)
	q.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{{Environment: "test", Bundle: "app-rollback-2", Reason: "r", CreatedBy: "bob", CreatedAt: &at2}}
	require.NoError(t, c.Update(context.Background(), q))
	res, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter, "no expiry to wait for")
	q = get()
	q.Spec.Holds = nil
	require.NoError(t, c.Update(context.Background(), q))
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	var actions []string
	for _, a := range holdAudits(t, c) {
		if a.Spec.BundleName == "app-rollback-2" {
			actions = append(actions, a.Spec.Action)
			if a.Spec.Action == "HoldReleased" {
				assert.Contains(t, a.Spec.Message, "released (spec.holds entry removed")
			}
		}
	}
	assert.ElementsMatch(t, []string{"HoldCreated", "HoldReleased"}, actions)
}

// failingAuditCreates is a client whose AuditEvent creates fail with an etcd
// timeout while fail is set.
type failingAuditCreates struct {
	client.Client
	fail bool
}

func (f *failingAuditCreates) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*kardinalv1alpha1.AuditEvent); ok && f.fail {
		return apierrors.NewInternalError(errors.New("etcdserver: request timed out"))
	}
	return f.Client.Create(ctx, obj, opts...)
}

// TestPipelineHolds_AuditOutbox (#1552): a HoldCreated AuditEvent whose
// create fails with an etcd timeout is kept in the Pipeline's
// status.pendingAuditEvents, stored with observedHolds, and the Pipeline is
// reconciled again within 5s. Once creates succeed the record is written,
// once, and the outbox is empty.
//
// Covers AUDIT-OUTBOX-01.
func TestPipelineHolds_AuditOutbox(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	at := metav1.NewTime(t0)
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{
		{Environment: "prod", Bundle: "app-rollback-1", Reason: "INC-42", CreatedBy: "alice", CreatedAt: &at},
	}
	c := &failingAuditCreates{Client: newClientWithIndex(newPipelineScheme(), p), fail: true}
	r := &pipeline.Reconciler{Client: c, Now: func() time.Time { return t0 }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "default"}}
	get := func() *kardinalv1alpha1.Pipeline {
		var got kardinalv1alpha1.Pipeline
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
		return &got
	}

	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, holdAudits(t, c))
	got := get()
	assert.Len(t, got.Status.ObservedHolds, 1, "the hold is recorded")
	require.Len(t, got.Status.PendingAuditEvents, 1, "with its AuditEvent in the outbox")
	assert.Equal(t, "HoldCreated", got.Status.PendingAuditEvents[0].Spec.Action)
	assert.Positive(t, res.RequeueAfter)
	assert.LessOrEqual(t, res.RequeueAfter, 5*time.Second)

	c.fail = false
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	ae := holdAudits(t, c)
	require.Len(t, ae, 1)
	assert.Equal(t, "HoldCreated", ae[0].Spec.Action)
	assert.Empty(t, get().Status.PendingAuditEvents)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Len(t, holdAudits(t, c), 1, "written once")
}

// staleGet serves the Pipeline from a stale copy while stale is set, as a
// lagging informer cache does.
type staleGet struct {
	client.Client
	stale   *kardinalv1alpha1.Pipeline
	creates int
}

func (s *staleGet) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*kardinalv1alpha1.AuditEvent); ok {
		s.creates++
	}
	return s.Client.Create(ctx, obj, opts...)
}

func (s *staleGet) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if p, ok := obj.(*kardinalv1alpha1.Pipeline); ok && s.stale != nil && key.Name == s.stale.Name {
		s.stale.DeepCopyInto(p)
		return nil
	}
	return s.Client.Get(ctx, key, obj, opts...)
}

// TestPipelineHolds_StaleReadStoresNoSecondRecord (#1552 QA): a reconcile
// that reads the Pipeline from before a newer reconcile recorded a hold sees
// the hold as new. Its patch carries the resourceVersion it read, so it gets
// a Conflict, stores nothing in the outbox and runs again shortly: one
// HoldCreated record.
//
// Covers AUDIT-OUTBOX-01.
func TestPipelineHolds_StaleReadStoresNoSecondRecord(t *testing.T) {
	t0 := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	at := metav1.NewTime(t0)
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{
		{Environment: "prod", Bundle: "app-rollback-1", Reason: "INC-42", CreatedBy: "alice", CreatedAt: &at},
	}
	c := &staleGet{Client: newClientWithIndex(newPipelineScheme(), p)}
	r := &pipeline.Reconciler{Client: c, Now: func() time.Time { return t0 }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "default"}}

	var before kardinalv1alpha1.Pipeline
	require.NoError(t, c.Client.Get(context.Background(), req.NamespacedName, &before))
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, holdAudits(t, c), 1)

	c.stale = before.DeepCopy()
	c.creates = 0
	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err, "a Conflict is retried, not returned")
	assert.Positive(t, res.RequeueAfter)
	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Client.Get(context.Background(), req.NamespacedName, &got))
	assert.Empty(t, got.Status.PendingAuditEvents, "the stale reconcile stored nothing")
	assert.Zero(t, c.creates, "and wrote no AuditEvent again")
	assert.Len(t, holdAudits(t, c), 1, "one HoldCreated")
}

// holdBundle is a rollback Bundle of the app Pipeline in default.
func holdBundle(name string) *kardinalv1alpha1.Bundle {
	return &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app"}}
}

// missingFixture is a Pipeline holding prod on a Bundle, with a fake
// recorder and a clock.
type missingFixture struct {
	t   *testing.T
	c   client.Client
	r   *pipeline.Reconciler
	rec *events.FakeRecorder
	now time.Time
	req ctrl.Request
}

func newMissingFixture(t *testing.T, hold kardinalv1alpha1.EnvironmentHold, objs ...client.Object) *missingFixture {
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{hold}
	f := &missingFixture{t: t, c: newClientWithIndex(newPipelineScheme(), append(objs, p)...),
		rec: events.NewFakeRecorder(20), now: time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC),
		req: ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "default"}}}
	f.r = &pipeline.Reconciler{Client: f.c, Recorder: f.rec, Now: func() time.Time { return f.now }}
	return f
}

func (f *missingFixture) reconcile() ctrl.Result {
	f.t.Helper()
	res, err := f.r.Reconcile(context.Background(), f.req)
	require.NoError(f.t, err)
	return res
}

func (f *missingFixture) get() *kardinalv1alpha1.Pipeline {
	f.t.Helper()
	var p kardinalv1alpha1.Pipeline
	require.NoError(f.t, f.c.Get(context.Background(), f.req.NamespacedName, &p))
	return &p
}

func (f *missingFixture) state() kardinalv1alpha1.EnvironmentHoldState {
	f.t.Helper()
	p := f.get()
	require.Len(f.t, p.Status.HoldStates, 1)
	return p.Status.HoldStates[0]
}

// signals counts the HoldBundleMissing Warning Events, AuditEvents and
// metric increments so far.
func (f *missingFixture) signals(before float64) (events, audits int, metric float64) {
	f.t.Helper()
	for len(f.rec.Events) > 0 {
		if ev := <-f.rec.Events; strings.Contains(ev, "Warning HoldBundleMissing") {
			events++
		}
	}
	for _, a := range holdAudits(f.t, f.c) {
		if a.Spec.Action == pipeline.AuditActionHoldBundleMissing {
			audits++
		}
	}
	return events, audits, testutil.ToFloat64(observability.HoldBundleMissingTotal.WithLabelValues("default", "app")) - before
}

// TestPipelineHolds_BundleMissing (#1629): a hold whose rollback Bundle does
// not exist stays in effect. It is BundleMissing from the controller's first
// sighting (not the client's createdAt); past the grace the controller
// reports it once: the HoldBundleMissing condition naming the environment,
// the Bundle and the release command, one Warning Event, one AuditEvent
// through the outbox and one metric increment, none repeated on later
// reconciles. A Bundle that exists again makes the hold Active and the
// condition False.
//
// Covers RB-HOLD-04.
func TestPipelineHolds_BundleMissing(t *testing.T) {
	// createdAt far in the past: the grace still counts from the sighting.
	old := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	f := newMissingFixture(t, kardinalv1alpha1.EnvironmentHold{Environment: "prod", Bundle: "app-rollback-gone",
		Reason: "INC-42", CreatedBy: "alice", CreatedAt: &old})
	metric := testutil.ToFloat64(observability.HoldBundleMissingTotal.WithLabelValues("default", "app"))
	t0 := f.now

	res := f.reconcile()
	st := f.state()
	assert.Equal(t, kardinalv1alpha1.HoldStateBundleMissing, st.State)
	assert.Equal(t, t0, st.BundleMissingSince.UTC(), "from the controller's first sighting")
	assert.Nil(t, st.ReportedAt)
	assert.Equal(t, 2*time.Minute, res.RequeueAfter, "back when the grace ends")
	assert.NotNil(t, lifecycle.HoldOf(f.get(), "prod"), "the hold stays in effect")
	cond := meta.FindStatusCondition(f.get().Status.Conditions, "HoldBundleMissing")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status, "not reported within the grace")

	f.now = t0.Add(2 * time.Minute)
	f.reconcile()
	f.reconcile() // the outbox writes the AuditEvent
	st = f.state()
	require.NotNil(t, st.ReportedAt)
	assert.Contains(t, st.Message, "rollback Bundle app-rollback-gone does not exist (missing since 2026-10-09T10:00:00Z); the hold stays in effect")
	assert.Contains(t, st.Message, "Release it with: kardinal release-hold app --env prod")
	got := f.get()
	cond = meta.FindStatusCondition(got.Status.Conditions, "HoldBundleMissing")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, "BundleMissing", cond.Reason)
	assert.Contains(t, cond.Message, "prod: rollback Bundle app-rollback-gone does not exist")
	assert.Contains(t, cond.Message, "kardinal release-hold app --env prod")
	require.NotNil(t, lifecycle.HoldOf(got, "prod"), "still in effect: the controller never lifts a hold")
	assert.NotNil(t, lifecycle.HeldFrom(got, "prod", "app-v2"), "other Bundles stay held back")
	require.Len(t, got.Spec.Holds, 1, "the controller does not edit spec.holds")
	ev, au, m := f.signals(metric)
	assert.Equal(t, [3]float64{1, 1, 1}, [3]float64{float64(ev), float64(au), m}, "one Event, one AuditEvent, one increment")
	audits := holdAudits(t, f.c)
	for _, a := range audits {
		if a.Spec.Action == pipeline.AuditActionHoldBundleMissing {
			assert.Equal(t, "Failure", a.Spec.Outcome)
			assert.Contains(t, a.Spec.Message, "the hold stays in effect until: kardinal release-hold app --env prod")
		}
	}

	// Later reconciles repeat nothing.
	for i := 0; i < 3; i++ {
		f.now = f.now.Add(time.Minute)
		f.reconcile()
	}
	ev, au, m = f.signals(metric)
	assert.Equal(t, [3]float64{0, 1, 1}, [3]float64{float64(ev), float64(au), m}, "no second report")

	// The Bundle exists again: Active, and the condition is False.
	require.NoError(t, f.c.Create(context.Background(), holdBundle("app-rollback-gone")))
	f.reconcile()
	assert.Equal(t, kardinalv1alpha1.HoldStateActive, f.state().State)
	cond = meta.FindStatusCondition(f.get().Status.Conditions, "HoldBundleMissing")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
}

// TestPipelineHolds_DeletedBundleKeepsHold (#1629 QA HIGH): deleting an
// Active hold's Bundle, by someone without pipelines/hold, does not lift the
// hold: it stays in effect, past the grace too, until it is released.
// Covers RB-HOLD-04.
func TestPipelineHolds_DeletedBundleKeepsHold(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	f := newMissingFixture(t, kardinalv1alpha1.EnvironmentHold{Environment: "prod", Bundle: "app-rollback-1",
		Reason: "INC-42", CreatedAt: &at}, holdBundle("app-rollback-1"))
	f.reconcile()
	assert.Equal(t, kardinalv1alpha1.HoldStateActive, f.state().State)

	require.NoError(t, f.c.Delete(context.Background(), holdBundle("app-rollback-1")))
	f.reconcile()
	assert.Equal(t, kardinalv1alpha1.HoldStateBundleMissing, f.state().State)
	assert.NotNil(t, lifecycle.HeldFrom(f.get(), "prod", "app-v2"), "within the grace the hold holds")
	f.now = f.now.Add(10 * time.Minute)
	f.reconcile()
	assert.NotNil(t, f.state().ReportedAt, "reported")
	assert.NotNil(t, lifecycle.HeldFrom(f.get(), "prod", "app-v2"), "past the grace the hold still holds")

	// Releasing it is the way out.
	_, err := lifecycle.ReleaseHold(context.Background(), f.c, "default", "app", "prod")
	require.NoError(t, err)
	f.reconcile()
	assert.Empty(t, f.get().Status.HoldStates)
	cond := meta.FindStatusCondition(f.get().Status.Conditions, "HoldBundleMissing")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
}

// TestPipelineHolds_ReaddedHoldReportedAgain (#1631 QA): a hold whose
// missing Bundle was reported, released and added again (the same
// environment and Bundle, a new createdAt) is a new hold: its state starts
// over and it gets a report of its own.
// Covers RB-HOLD-04.
func TestPipelineHolds_ReaddedHoldReportedAgain(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	f := newMissingFixture(t, kardinalv1alpha1.EnvironmentHold{Environment: "prod", Bundle: "app-rollback-gone",
		Reason: "INC-42", CreatedAt: &at})
	metric := testutil.ToFloat64(observability.HoldBundleMissingTotal.WithLabelValues("default", "app"))
	f.reconcile()
	f.now = f.now.Add(2 * time.Minute)
	f.reconcile()
	f.reconcile()
	ev, au, m := f.signals(metric)
	require.Equal(t, [3]float64{1, 1, 1}, [3]float64{float64(ev), float64(au), m})

	// Released, then held again on the same Bundle name.
	_, err := lifecycle.ReleaseHold(context.Background(), f.c, "default", "app", "prod")
	require.NoError(t, err)
	at2 := metav1.NewTime(f.now)
	p := f.get()
	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "app-rollback-gone", Reason: "INC-43", CreatedAt: &at2}}
	require.NoError(t, f.c.Update(context.Background(), p))
	f.reconcile()
	st := f.state()
	assert.Nil(t, st.ReportedAt, "a new hold: not reported yet")
	assert.Equal(t, f.now, st.BundleMissingSince.UTC(), "its own first sighting")
	f.now = f.now.Add(2 * time.Minute)
	f.reconcile()
	f.reconcile()
	ev, au, m = f.signals(metric)
	assert.Equal(t, [3]float64{1, 2, 2}, [3]float64{float64(ev), float64(au), m}, "a report of its own")
}

// failingBundleGets fails every Bundle read with a server error.
type failingBundleGets struct{ client.Client }

func (f failingBundleGets) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*kardinalv1alpha1.Bundle); ok {
		return apierrors.NewInternalError(errors.New("etcdserver: request timed out"))
	}
	return f.Client.Get(ctx, key, obj, opts...)
}

// TestPipelineHolds_BundleLookupFails: a Bundle read that fails for another
// reason than NotFound keeps the hold's state, sets the condition Unknown,
// and does not stop the rest of the reconcile.
// Covers RB-HOLD-04.
func TestPipelineHolds_BundleLookupFails(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	f := newMissingFixture(t, kardinalv1alpha1.EnvironmentHold{Environment: "prod", Bundle: "app-rollback-1",
		Reason: "r", CreatedAt: &at}, holdBundle("app-rollback-1"))
	f.reconcile()
	f.r.Client = failingBundleGets{f.c}
	f.now = f.now.Add(10 * time.Minute)
	_, err := f.r.Reconcile(context.Background(), f.req)
	require.NoError(t, err, "the rest of the reconcile goes on")
	got := f.get()
	assert.Equal(t, kardinalv1alpha1.HoldStateActive, got.Status.HoldStates[0].State, "the state is kept")
	cond := meta.FindStatusCondition(got.Status.Conditions, "HoldBundleMissing")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, "LookupFailed", cond.Reason)
	assert.Contains(t, cond.Message, "the holds are kept; prod: get rollback Bundle app-rollback-1")
	assert.NotNil(t, lifecycle.HoldOf(got, "prod"))
	assert.NotEmpty(t, got.Status.Phase, "the Pipeline's status was still derived")
}

// TestPipelineHolds_GraceFlag: --hold-bundle-grace sets the grace.
//
// Covers RB-HOLD-04.
func TestPipelineHolds_GraceFlag(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	f := newMissingFixture(t, kardinalv1alpha1.EnvironmentHold{Environment: "prod", Bundle: "app-rollback-x", Reason: "r", CreatedAt: &at})
	f.r.HoldBundleGrace = 30 * time.Second
	t0 := f.now
	f.reconcile()
	f.now = t0.Add(29 * time.Second)
	f.reconcile()
	assert.Nil(t, f.state().ReportedAt)
	f.now = t0.Add(30 * time.Second)
	f.reconcile()
	assert.NotNil(t, f.state().ReportedAt)
}
