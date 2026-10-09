// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// auditEvents returns the AuditEvents in the namespace, oldest first.
func auditEvents(t *testing.T, c client.Client) []kardinalv1alpha1.AuditEvent {
	t.Helper()
	var list kardinalv1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("default")))
	sort.Slice(list.Items, func(i, j int) bool {
		return lifecycle.CompareAuditEvents(&list.Items[i], &list.Items[j]) < 0
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
	assert.True(t, strings.HasSuffix(events[0].Name, "-gate-success-1775556000000"), events[0].Name)
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

// TestPolicyGateReconciler_AuditInTerminatingNamespace: a namespace being
// deleted refuses the AuditEvent, and its deletion removes the gate next. The
// refusal is logged at debug, not as a failed write.
func TestPolicyGateReconciler_AuditInTerminatingNamespace(t *testing.T) {
	gate := makeGateInstance("prod-no-weekend", "default", "nginx-demo-v1", "true", "5m")
	terminating := apierrors.NewForbidden(schema.GroupResource{Group: "kardinal.io", Resource: "auditevents"}, "x",
		errors.New("unable to create new content in namespace default because it is being terminated"))
	terminating.ErrStatus.Details.Causes = append(terminating.ErrStatus.Details.Causes, metav1.StatusCause{
		Type: corev1.NamespaceTerminatingCause, Field: "metadata.namespace",
		Message: "namespace default is being terminated"})
	c := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(gate, makeBundle("nginx-demo-v1", "default")).
		WithStatusSubresource(gate).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*kardinalv1alpha1.AuditEvent); ok {
					return terminating
				}
				return c.Create(ctx, obj, opts...)
			},
		}).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)

	var logs bytes.Buffer
	ctx := zerolog.New(&logs).WithContext(context.Background())
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: gate.Name}})
	require.NoError(t, err)
	assert.Contains(t, logs.String(), "being terminated")
	assert.NotContains(t, logs.String(), `"level":"warn"`)
	assert.NotContains(t, logs.String(), `"level":"error"`)
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

// TestPolicyGateReconciler_AuditOutboxEtcdTimeout proves #1552 for gates: a
// GateEvaluated create that fails with an etcd timeout is kept in
// status.pendingAuditEvents, stored with the flip, and the reconcile is
// requeued. Once creates succeed the next reconcile writes it, even though
// the result is unchanged and nothing else would be written, and empties
// the outbox.
//
// Covers AUDIT-OUTBOX-01.
func TestPolicyGateReconciler_AuditOutboxEtcdTimeout(t *testing.T) {
	tue := time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC)
	gate := makeGateInstance("prod-no-weekend", "default", "nginx-demo-v1", "!schedule.isWeekend", "5m")
	var failing atomic.Bool
	failing.Store(true)
	c := fake.NewClientBuilder().WithScheme(newScheme()).
		WithObjects(gate, makeBundle("nginx-demo-v1", "default")).
		WithStatusSubresource(gate).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*kardinalv1alpha1.AuditEvent); ok && failing.Load() {
					return apierrors.NewInternalError(errors.New("etcdserver: request timed out"))
				}
				return c.Create(ctx, obj, opts...)
			},
		}).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	now := tue
	r.NowFn = func() time.Time { return now }
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: gate.Name}}
	get := func() kardinalv1alpha1.PolicyGate {
		var got kardinalv1alpha1.PolicyGate
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
		return got
	}

	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	got := get()
	assert.True(t, got.Status.Ready, "the audit write never blocks the evaluation")
	require.Len(t, got.Status.PendingAuditEvents, 1, "the record is stored with the flip")
	assert.Equal(t, "Success", got.Status.PendingAuditEvents[0].Spec.Outcome)
	assert.True(t, tue.Equal(got.Status.PendingAuditEvents[0].Spec.Timestamp.Time))
	assert.Empty(t, auditEvents(t, c))
	assert.LessOrEqual(t, res.RequeueAfter, 5*time.Second, "requeued to retry the record")
	assert.Positive(t, res.RequeueAfter)

	failing.Store(false)
	now = tue.Add(time.Minute) // a recheck with the same result
	res, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	events := auditEvents(t, c)
	require.Len(t, events, 1)
	assert.True(t, tue.Equal(events[0].Spec.Timestamp.Time), "the record carries the flip's time, not the write's")
	assert.Empty(t, get().Status.PendingAuditEvents)
	assert.Greater(t, res.RequeueAfter, 5*time.Second, "back to the recheck interval")
}
