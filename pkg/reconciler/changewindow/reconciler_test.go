// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changewindow_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestReconciler_NotFound(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	r := &changewindow.Reconciler{Client: fake.NewClientBuilder().WithScheme(s).Build()}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "gone"}})
	require.NoError(t, err)
}
