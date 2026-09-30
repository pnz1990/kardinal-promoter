// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestInstanceGateRequests covers the watch mappers: only instance gates are
// enqueued, the ChangeWindow mapper only enqueues gates that reference
// changewindow, and a List error returns no requests (C04-gates-36).
func TestInstanceGateRequests(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(scheme))
	gate := func(name, ns, bundle, expr string) *kardinalv1alpha1.PolicyGate {
		g := &kardinalv1alpha1.PolicyGate{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{}},
			Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: expr},
		}
		if bundle != "" {
			g.Labels[labelBundle] = bundle
		}
		return g
	}
	objs := []client.Object{
		gate("cw-a", "team-a", "b1", `!changewindow["freeze"]`),
		gate("cw-b", "team-b", "b2", `changewindow.isAllowed("hours")`),
		gate("weekend", "team-a", "b1", `!schedule.isWeekend`),
		gate("template", "team-a", "", `!changewindow["freeze"]`),
	}
	req := func(name, ns string) reconcile.Request {
		return reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
	}

	r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()}
	ctx := context.Background()
	assert.ElementsMatch(t, []reconcile.Request{req("cw-a", "team-a"), req("cw-b", "team-b")},
		r.instanceGateRequests(ctx, "ChangeWindow", "changewindow"))
	assert.ElementsMatch(t, []reconcile.Request{req("cw-a", "team-a"), req("weekend", "team-a"), req("cw-b", "team-b")},
		r.instanceGateRequests(ctx, "ScheduleClock", ""))
	assert.ElementsMatch(t, []reconcile.Request{req("cw-a", "team-a"), req("weekend", "team-a")},
		r.instanceGateRequests(ctx, "MetricCheck", "", client.InNamespace("team-a")))

	failing := &Reconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				return fmt.Errorf("list failed")
			},
		}).Build()}
	assert.Empty(t, failing.instanceGateRequests(ctx, "ChangeWindow", "changewindow"))
}
