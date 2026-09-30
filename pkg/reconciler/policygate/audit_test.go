// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// auditEvents returns the AuditEvents in the namespace, oldest first.
func auditEvents(t *testing.T, c client.Client) []kardinalv1alpha1.AuditEvent {
	t.Helper()
	var list kardinalv1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("default")))
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Spec.Timestamp.Before(&list.Items[j].Spec.Timestamp)
	})
	return list.Items
}

// TestPolicyGateReconciler_AuditsEveryTransition covers C04-gates-22: every
// ready flip gets its own AuditEvent (the fixed name used to record only the
// first), rechecks with an unchanged result are not audited, and the event
// carries the user-facing gate name.
func TestPolicyGateReconciler_AuditsEveryTransition(t *testing.T) {
	tue := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	sat := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		steps        []time.Time
		wantOutcomes []string
	}{
		{name: "allow, block, allow", steps: []time.Time{tue, sat, tue.Add(7 * 24 * time.Hour)}, wantOutcomes: []string{"Success", "Failure", "Success"}},
		{name: "blocks on first evaluation", steps: []time.Time{sat, sat.Add(5 * time.Minute), tue.Add(7 * 24 * time.Hour)}, wantOutcomes: []string{"Failure", "Success"}},
		{name: "rechecks are not transitions", steps: []time.Time{tue, tue.Add(5 * time.Minute), tue.Add(10 * time.Minute)}, wantOutcomes: []string{"Success"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := makeGateInstance("prod-no-weekend", "default", "nginx-demo-v1", "!schedule.isWeekend", "5m")
			gate.Labels["kardinal.io/gate-name"] = "no-weekend-deploys"
			gate.Labels["kardinal.io/gate-template"] = "no-weekend-deploys"
			c := fake.NewClientBuilder().WithScheme(newScheme()).
				WithObjects(gate, makeBundle("nginx-demo-v1", "default")).
				WithStatusSubresource(gate).Build()
			var now time.Time
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return now }

			for _, at := range tt.steps {
				now = at
				_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: gate.Name}})
				require.NoError(t, err)
			}

			events := auditEvents(t, c)
			var outcomes []string
			for _, ae := range events {
				outcomes = append(outcomes, ae.Spec.Outcome)
				assert.Equal(t, "GateEvaluated", ae.Spec.Action)
				assert.Equal(t, "no-weekend-deploys", ae.Labels["kardinal.io/gate"])
				assert.True(t, strings.HasPrefix(ae.Name, "prod-no-weekend-gate-"), ae.Name)
			}
			assert.Equal(t, tt.wantOutcomes, outcomes)
		})
	}
}

// TestPolicyGateReconciler_AuditNameAndLabelFallback: a gate name at the
// length limit still gets a valid, unique AuditEvent name, and an instance
// without kardinal.io/gate-name falls back to kardinal.io/gate-template.
func TestPolicyGateReconciler_AuditNameAndLabelFallback(t *testing.T) {
	tue := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	gate := makeGateInstance(strings.Repeat("g", 253), "default", "nginx-demo-v1", "!schedule.isWeekend", "5m")
	gate.Labels["kardinal.io/gate-template"] = "no-weekend-deploys"
	c := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(gate, makeBundle("nginx-demo-v1", "default")).
		WithStatusSubresource(gate).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return tue }
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: gate.Name}})
	require.NoError(t, err)

	events := auditEvents(t, c)
	require.Len(t, events, 1)
	assert.LessOrEqual(t, len(events[0].Name), 253)
	assert.True(t, strings.HasSuffix(events[0].Name, "-gate-success-1775556000"), events[0].Name)
	assert.Equal(t, "no-weekend-deploys", events[0].Labels["kardinal.io/gate"])
	assert.True(t, tue.Equal(events[0].Spec.Timestamp.Time), "the timestamp is the evaluation time")
}

// TestPolicyGateReconciler_AuditWriteErrorIsLogged: a failed AuditEvent write
// does not fail the evaluation, and is logged rather than dropped.
func TestPolicyGateReconciler_AuditWriteErrorIsLogged(t *testing.T) {
	tue := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	gate := makeGateInstance("prod-no-weekend", "default", "nginx-demo-v1", "!schedule.isWeekend", "5m")
	c := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(gate, makeBundle("nginx-demo-v1", "default")).
		WithStatusSubresource(gate).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*kardinalv1alpha1.AuditEvent); ok {
					return apierrors.NewForbidden(schema.GroupResource{Group: "kardinal.io", Resource: "auditevents"}, obj.GetName(), nil)
				}
				return c.Create(ctx, obj, opts...)
			},
		}).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return tue }

	var logs bytes.Buffer
	ctx := zerolog.New(&logs).WithContext(context.Background())
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: gate.Name}})
	require.NoError(t, err)

	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: gate.Name}, &got))
	assert.True(t, got.Status.Ready)
	assert.Contains(t, logs.String(), "failed to write PolicyGate AuditEvent")
	assert.Contains(t, logs.String(), "forbidden")
}

// TestReconciler_OverrideRequeuesAtExpiry covers C04-gates-21: while an
// override is active the gate is re-evaluated just after it expires, not a
// whole recheck interval later.
func TestReconciler_OverrideRequeuesAtExpiry(t *testing.T) {
	now := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		expiresIn time.Duration
		recheck   string
		want      time.Duration
	}{
		{name: "expires before the recheck", expiresIn: time.Minute, recheck: "5m", want: time.Minute + time.Second},
		{name: "expires after the recheck", expiresIn: time.Hour, recheck: "5m", want: 5 * time.Minute},
		{name: "long recheck interval", expiresIn: 10 * time.Minute, recheck: "6h", want: 10*time.Minute + time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := makeGateInstance("ov", "default", "app-v1", "false", tt.recheck)
			gate.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{
				{Reason: "hotfix", Stage: "prod", ExpiresAt: metav1.NewTime(now.Add(tt.expiresIn)), CreatedBy: "alice"},
			}
			c := fake.NewClientBuilder().WithScheme(newScheme()).
				WithObjects(gate, makeBundle("app-v1", "default")).
				WithStatusSubresource(gate).Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return now }

			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "ov"}})
			require.NoError(t, err)
			assert.Equal(t, tt.want, res.RequeueAfter)

			// After expiry the override no longer applies.
			r.NowFn = func() time.Time { return now.Add(res.RequeueAfter) }
			if res.RequeueAfter > tt.expiresIn {
				_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "ov"}})
				require.NoError(t, err)
				var got kardinalv1alpha1.PolicyGate
				require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "ov"}, &got))
				assert.False(t, got.Status.Ready, "the expired override no longer force-passes the gate")
			}
		})
	}
}
