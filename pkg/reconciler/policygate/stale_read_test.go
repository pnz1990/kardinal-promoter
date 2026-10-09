// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// TestPolicyGateReconciler_StaleReadWritesNoDuplicateAudit (#1513): a
// reconcile that reads the gate from a stale cache, from before the flip a
// newer reconcile already wrote, would detect that flip again and audit it a
// second time milliseconds later. The status patch carries the
// resourceVersion it read, so the stale reconcile gets a Conflict, writes
// nothing and is requeued; the next reconcile on the current gate writes
// nothing either (unchanged result).
//
// Covers GATE-AUDIT-03.
func TestPolicyGateReconciler_StaleReadWritesNoDuplicateAudit(t *testing.T) {
	tue := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	gate := makeGateInstance("prod-no-weekend", "default", "nginx-demo-v1", "!schedule.isWeekend", "5m")
	base := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(gate, makeBundle("nginx-demo-v1", "default")).
		WithStatusSubresource(gate).Build()
	key := types.NamespacedName{Namespace: "default", Name: gate.Name}

	var stale *kardinalv1alpha1.PolicyGate
	c := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if g, ok := obj.(*kardinalv1alpha1.PolicyGate); ok && stale != nil && k == key {
				stale.DeepCopyInto(g)
				return nil
			}
			return cl.Get(ctx, k, obj, opts...)
		},
	})
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	now := tue
	r.NowFn = func() time.Time { return now }

	var before kardinalv1alpha1.PolicyGate
	require.NoError(t, base.Get(context.Background(), key, &before))
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	require.Len(t, auditEvents(t, base), 1, "the first evaluation is audited")

	// The cache still holds the gate from before the first evaluation.
	stale = before.DeepCopy()
	now = tue.Add(20 * time.Millisecond)
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err, "a Conflict is requeued, not returned")
	assert.True(t, res.Requeue, "the stale reconcile is requeued") //nolint:staticcheck // Requeue is what Reconcile returns
	assert.Len(t, auditEvents(t, base), 1, "the stale reconcile writes no second record")

	stale = nil
	now = tue.Add(40 * time.Millisecond)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Len(t, auditEvents(t, base), 1, "the current gate has the result already")
}
