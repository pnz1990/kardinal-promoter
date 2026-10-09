// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// TestReconciler_GatesEvaluateSideBySide (#1509): one Reconciler, as with
// --policygate-workers above 1, evaluates many gates at once, sharing its
// CEL program cache, and writes each gate's own result and one audit
// record per gate. Run with -race.
//
// Covers PERF-WORKERS-01.
func TestReconciler_GatesEvaluateSideBySide(t *testing.T) {
	const n = 24
	tue := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	var objs []client.Object
	for i := 0; i < n; i++ {
		expr := []string{"!schedule.isWeekend", "schedule.hour >= 9", "bundle.type != 'nope'", "schedule.hour > 23"}[i%4]
		objs = append(objs, makeGateInstance(fmt.Sprintf("prod-g%d", i), "default", fmt.Sprintf("app-v%d", i), expr, "5m"),
			makeBundle(fmt.Sprintf("app-v%d", i), "default"))
	}
	b := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(objs...)
	for _, o := range objs {
		if g, ok := o.(*kardinalv1alpha1.PolicyGate); ok {
			b = b.WithStatusSubresource(g)
		}
	}
	c := b.Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return tue }
	r.Workers = 8

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: name}})
			assert.NoError(t, err, name)
		}(fmt.Sprintf("prod-g%d", i))
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		var g kardinalv1alpha1.PolicyGate
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: fmt.Sprintf("prod-g%d", i)}, &g))
		require.NotNil(t, g.Status.LastEvaluatedAt, g.Name)
		assert.Equal(t, i%4 != 3, g.Status.Ready, "%s: %s", g.Name, g.Status.Reason)
	}
	assert.Len(t, auditEvents(t, c), n, "one record per gate")
}
