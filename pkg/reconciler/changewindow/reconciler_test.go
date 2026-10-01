// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changewindow_test

import (
	"bytes"
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
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone/objectgonetest"
)

// TestReconciler_WritesStatusAndRequeuesAtBoundary verifies that the reconciler
// writes status.active/reason and requeues just after the next boundary, and
// that re-running it is a no-op (C04-gates-03, C08-api-config-05).
func TestReconciler_WritesStatusAndRequeuesAtBoundary(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	cw := &kardinalv1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "business-hours"},
		Spec:       recurring("", []string{"Mon", "Tue", "Wed", "Thu", "Fri"}, "09:00-17:00"),
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cw).WithStatusSubresource(cw).Build()

	now := at("2026-10-02T16:30:00Z") // Friday, inside the window
	r := &changewindow.Reconciler{Client: c, NowFn: func() time.Time { return now }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "business-hours"}}

	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Minute+time.Second, res.RequeueAfter, "requeue just after 17:00")
	var got kardinalv1alpha1.ChangeWindow
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.False(t, got.Status.Active)
	assert.Contains(t, got.Status.Reason, "inside the allowed window")
	rv := got.ResourceVersion

	// Idempotent: same state, no write.
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Equal(t, rv, got.ResourceVersion, "unchanged status is not rewritten")

	// After the boundary the window blocks.
	now = at("2026-10-02T17:00:01Z")
	res, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.True(t, got.Status.Active)
	assert.Contains(t, got.Status.Reason, "outside the allowed window")
	assert.Equal(t, time.Hour, res.RequeueAfter, "requeue is capped")
}

func TestReconciler_InvalidSpecIsActive(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	cw := &kardinalv1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "broken"},
		Spec:       kardinalv1alpha1.ChangeWindowSpec{Type: "recurring"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cw).WithStatusSubresource(cw).Build()
	r := &changewindow.Reconciler{Client: c, NowFn: func() time.Time { return at("2026-10-02T16:00:00Z") }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "broken"}}

	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
	var got kardinalv1alpha1.ChangeWindow
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.True(t, got.Status.Active)
	assert.Contains(t, got.Status.Reason, "requires spec.schedule")
}

// TestReconciler_ValidCondition: an unknown timezone makes the window active,
// and the Valid condition says why (a gate that references the window only
// shows that its expression is false). Fixing the spec flips the condition,
// and re-running without a change writes nothing.
func TestReconciler_ValidCondition(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	weekdays := []string{"Mon", "Tue", "Wed", "Thu", "Fri"}
	cw := &kardinalv1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "business-hours", Generation: 1},
		Spec:       recurring("America/Los_Angles", weekdays, "09:00-17:00"),
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cw).WithStatusSubresource(cw).Build()
	now := at("2026-10-02T16:30:00Z") // Friday 09:30 in Los Angeles
	r := &changewindow.Reconciler{Client: c, NowFn: func() time.Time { return now }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "business-hours"}}
	ctx := context.Background()

	get := func() kardinalv1alpha1.ChangeWindow {
		t.Helper()
		var got kardinalv1alpha1.ChangeWindow
		require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
		return got
	}

	steps := []struct {
		name        string
		timezone    string
		wantActive  bool
		wantStatus  metav1.ConditionStatus
		wantReason  string
		wantMessage []string
	}{
		{name: "typo in timezone", timezone: "America/Los_Angles", wantActive: true,
			wantStatus: metav1.ConditionFalse, wantReason: "InvalidSpec",
			wantMessage: []string{`"America/Los_Angles" is not a known IANA timezone name`, "active (blocking)"}},
		{name: "fixed timezone", timezone: "America/Los_Angeles", wantActive: false,
			wantStatus: metav1.ConditionTrue, wantReason: "SpecValid"},
		{name: "controller local time", timezone: "Local", wantActive: true,
			wantStatus: metav1.ConditionFalse, wantReason: "InvalidSpec",
			wantMessage: []string{"controller's local time"}},
	}
	for i, st := range steps {
		if i > 0 {
			cur := get()
			cur.Spec.Schedule.Timezone = st.timezone
			cur.Generation++
			require.NoError(t, c.Update(ctx, &cur))
			now = now.Add(time.Minute)
		}
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err, st.name)
		got := get()
		assert.Equal(t, st.wantActive, got.Status.Active, st.name)
		cond := meta.FindStatusCondition(got.Status.Conditions, changewindow.ConditionValid)
		require.NotNil(t, cond, st.name)
		assert.Equal(t, st.wantStatus, cond.Status, st.name)
		assert.Equal(t, st.wantReason, cond.Reason, st.name)
		assert.Equal(t, got.Generation, cond.ObservedGeneration, st.name)
		assert.True(t, now.Equal(cond.LastTransitionTime.Time), "%s: the condition flipped now", st.name)
		for _, m := range st.wantMessage {
			assert.Contains(t, cond.Message, m, st.name)
		}

		rv := got.ResourceVersion
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, rv, get().ResourceVersion, "%s: an unchanged condition is not rewritten", st.name)
	}
}

// TestReconciler_InvalidSpecReportedOncePerGeneration: an invalid spec gets
// one Warning log line and one Warning Event per generation, however often
// the window is reconciled (boundary requeues, resyncs, annotation edits, a
// controller restart). A new generation that is still invalid is reported
// once more; a fixed spec reports nothing.
func TestReconciler_InvalidSpecReportedOncePerGeneration(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	weekdays := []string{"Mon", "Tue", "Wed", "Thu", "Fri"}
	cw := &kardinalv1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "business-hours", Generation: 1},
		Spec:       recurring("America/Los_Angles", weekdays, "09:00-17:00"),
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cw).WithStatusSubresource(cw).Build()
	now := at("2026-10-02T16:30:00Z")
	rec := events.NewFakeRecorder(20)
	var logs bytes.Buffer
	ctx := objectgonetest.Context(&logs)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "business-hours"}}
	newReconciler := func() *changewindow.Reconciler {
		return &changewindow.Reconciler{Client: c, Recorder: rec, NowFn: func() time.Time { return now }}
	}
	r := newReconciler()

	reported := func() (warnings int, evts []string) {
		t.Helper()
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, `"level":"warn"`) && strings.Contains(line, "invalid ChangeWindow") {
				warnings++
			}
		}
		logs.Reset()
		for {
			select {
			case e := <-rec.Events:
				evts = append(evts, e)
			default:
				return warnings, evts
			}
		}
	}
	reconcile := func(times int) {
		t.Helper()
		for range times {
			_, err := r.Reconcile(ctx, req)
			require.NoError(t, err)
			now = now.Add(time.Minute)
		}
	}
	edit := func(timezone string, bump bool) {
		t.Helper()
		var cur kardinalv1alpha1.ChangeWindow
		require.NoError(t, c.Get(ctx, req.NamespacedName, &cur))
		cur.Spec.Schedule.Timezone = timezone
		if bump {
			cur.Generation++
		} else {
			cur.Annotations = map[string]string{"edited": now.String()}
		}
		require.NoError(t, c.Update(ctx, &cur))
	}

	reconcile(5)
	warnings, evts := reported()
	assert.Equal(t, 1, warnings, "generation 1: one warning")
	require.Len(t, evts, 1, "generation 1: one Event")
	assert.Contains(t, evts[0], "Warning InvalidSpec")
	assert.Contains(t, evts[0], `"America/Los_Angles" is not a known IANA timezone name`)

	edit("America/Los_Angles", false) // an annotation edit keeps the generation
	reconcile(2)
	r = newReconciler() // a controller restart
	reconcile(2)
	warnings, evts = reported()
	assert.Zero(t, warnings, "same generation: no warning")
	assert.Empty(t, evts, "same generation: no Event")

	edit("Local", true) // generation 2, still invalid
	reconcile(3)
	warnings, evts = reported()
	assert.Equal(t, 1, warnings, "generation 2: one more warning")
	require.Len(t, evts, 1, "generation 2: one more Event")
	assert.Contains(t, evts[0], "controller's local time")

	edit("America/Los_Angeles", true) // generation 3 is valid
	reconcile(2)
	warnings, evts = reported()
	assert.Zero(t, warnings, "a valid spec: no warning")
	assert.Empty(t, evts, "a valid spec: no Event")

	edit("America/Los_Angles", true) // generation 4 is invalid again
	reconcile(2)
	warnings, evts = reported()
	assert.Equal(t, 1, warnings, "generation 4: one warning")
	assert.Len(t, evts, 1, "generation 4: one Event")
}

func TestReconciler_NotFound(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	r := &changewindow.Reconciler{Client: fake.NewClientBuilder().WithScheme(s).Build()}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "gone"}})
	require.NoError(t, err)
}

// TestReconciler_DeletedBeforeStatusWrite: a ChangeWindow deleted between its
// read and its status write ends the reconcile with no error, requeue or warn
// or error log.
func TestReconciler_DeletedBeforeStatusWrite(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	cw := &kardinalv1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "business-hours"},
		Spec:       recurring("", []string{"Mon", "Tue", "Wed", "Thu", "Fri"}, "09:00-17:00"),
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cw).WithStatusSubresource(cw).
		WithInterceptorFuncs(objectgonetest.DeleteOnWrite(t, nil)).Build()
	r := &changewindow.Reconciler{Client: c, NowFn: func() time.Time { return at("2026-10-02T16:30:00Z") }}
	var logs bytes.Buffer
	res, err := r.Reconcile(objectgonetest.Context(&logs), ctrl.Request{NamespacedName: types.NamespacedName{Name: "business-hours"}})
	objectgonetest.AssertQuiet(t, res, err, &logs)
}
