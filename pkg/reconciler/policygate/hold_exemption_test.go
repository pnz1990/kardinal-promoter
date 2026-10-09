// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// TestPolicyGateReconciler_HoldExemption (#1528): a blocking gate of the
// rollback Bundle the Pipeline holds an environment on passes, never
// silently: the reason starts with EXEMPT and names the hold, who made it,
// why, and the gate's own result; the flip is a GateEvaluated AuditEvent;
// one GateExempted Warning Event is emitted per exempt episode. A Bundle that
// is not a rollback, or that no hold names, gets no exemption, and releasing
// the hold blocks the gate again.
func TestPolicyGateReconciler_HoldExemption(t *testing.T) {
	const rb = "nginx-demo-rollback-abc123"
	hold := kardinalv1alpha1.EnvironmentHold{Environment: "prod", Bundle: rb, Reason: "INC-42", CreatedBy: "alice"}
	pipeline := func(holds ...kardinalv1alpha1.EnvironmentHold) *kardinalv1alpha1.Pipeline {
		return &kardinalv1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
			Spec: kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "prod"}}, Holds: holds}}
	}
	rollback := func(name string) *kardinalv1alpha1.Bundle {
		b := makeBundle(name, "default")
		b.Labels = map[string]string{lifecycle.LabelRollback: "true"}
		return b
	}
	tests := []struct {
		name       string
		bundle     *kardinalv1alpha1.Bundle
		pipeline   *kardinalv1alpha1.Pipeline
		wantExempt bool
	}{
		{name: "the held rollback passes", bundle: rollback(rb), pipeline: pipeline(hold), wantExempt: true},
		{name: "not a rollback: no exemption", bundle: makeBundle(rb, "default"), pipeline: pipeline(hold)},
		{name: "a rollback no hold names: no exemption", bundle: rollback("nginx-demo-rollback-other"), pipeline: pipeline(hold)},
		{name: "no hold: no exemption", bundle: rollback(rb), pipeline: pipeline()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			gate := makeGateInstance("prod-no-weekend", "default", tc.bundle.Name, "!schedule.isWeekend", "5m")
			gate.Labels["kardinal.io/gate-name"] = "no-weekend-deploys"
			c := fake.NewClientBuilder().WithScheme(newScheme()).
				WithObjects(gate, tc.bundle, tc.pipeline).WithStatusSubresource(gate).Build()
			rec := events.NewFakeRecorder(10)
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			sat := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
			now := sat
			r.NowFn = func() time.Time { return now }
			r.Recorder = rec
			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: gate.Name}}
			get := func() *kardinalv1alpha1.PolicyGate {
				var g kardinalv1alpha1.PolicyGate
				require.NoError(t, c.Get(ctx, req.NamespacedName, &g))
				return &g
			}

			_, err = r.Reconcile(ctx, req)
			require.NoError(t, err)
			g := get()
			if !tc.wantExempt {
				assert.False(t, g.Status.Ready, "the gate blocks")
				assert.False(t, lifecycle.IsHoldExemption(g.Status.Reason), g.Status.Reason)
				return
			}
			assert.True(t, g.Status.Ready, "the held rollback passes the blocking gate")
			assert.True(t, strings.HasPrefix(g.Status.Reason, "EXEMPT: rollback "+rb+" holds prod (by alice: INC-42); without the hold: "),
				g.Status.Reason)
			assert.Contains(t, g.Status.Reason, "!schedule.isWeekend = false", "the gate's own result stays visible")
			ae := auditEvents(t, c)
			require.Len(t, ae, 1)
			assert.Equal(t, "GateEvaluated", ae[0].Spec.Action)
			assert.Equal(t, "Success", ae[0].Spec.Outcome)
			assert.Contains(t, ae[0].Spec.Message, "EXEMPT", "the audit record says it was an exemption")
			var evs []string
			for len(rec.Events) > 0 {
				evs = append(evs, <-rec.Events)
			}
			var exempted []string
			for _, ev := range evs {
				if strings.Contains(ev, "GateExempted") {
					exempted = append(exempted, ev)
				}
			}
			require.Len(t, exempted, 1, "events: %q", evs)
			assert.True(t, strings.HasPrefix(exempted[0], "Warning GateExempted EXEMPT: rollback "+rb), exempted[0])

			// A recheck while the gate still blocks: no new Event, no new audit.
			now = sat.Add(time.Hour)
			_, err = r.Reconcile(ctx, req)
			require.NoError(t, err)
			for len(rec.Events) > 0 {
				assert.NotContains(t, <-rec.Events, "GateExempted", "one Event per exempt episode")
			}
			assert.Len(t, auditEvents(t, c), 1)

			// Released: the gate blocks the rollback again, audited.
			_, err = lifecycle.ReleaseHold(ctx, c, "default", "nginx-demo", "prod")
			require.NoError(t, err)
			_, err = r.Reconcile(ctx, req)
			require.NoError(t, err)
			g = get()
			assert.False(t, g.Status.Ready)
			assert.False(t, lifecycle.IsHoldExemption(g.Status.Reason))
			ae = auditEvents(t, c)
			require.Len(t, ae, 2)
			assert.Equal(t, "Failure", ae[1].Spec.Outcome)
		})
	}
}
