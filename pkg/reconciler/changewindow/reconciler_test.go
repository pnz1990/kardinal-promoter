// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changewindow_test

import (
	"context"
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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
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

func TestReconciler_NotFound(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	r := &changewindow.Reconciler{Client: fake.NewClientBuilder().WithScheme(s).Build()}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "gone"}})
	require.NoError(t, err)
}
