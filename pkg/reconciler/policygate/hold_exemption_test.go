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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

const holdRB = "nginx-demo-rollback-abc123"

// holdFixture is pipeline nginx-demo with v1 (tag 1) Verified in prod, the
// rollback rb of v1 (same images) and a hold of prod on it with the digest
// of rb's artifacts.
type holdFixture struct {
	pipeline *kardinalv1alpha1.Pipeline
	target   *kardinalv1alpha1.Bundle
	rb       *kardinalv1alpha1.Bundle
	steps    []client.Object
}

func newHoldFixture() *holdFixture {
	img := []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "1"}}
	target := makeBundle("nginx-demo-v1", "default")
	target.Spec.Images = img
	rb := makeBundle(holdRB, "default")
	rb.Labels = map[string]string{lifecycle.LabelRollback: "true"}
	rb.Spec.Images = append([]kardinalv1alpha1.ImageRef(nil), img...)
	rb.Spec.Provenance.RollbackOf = target.Name
	step := &kardinalv1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo-v1-prod", Namespace: "default",
			Labels: map[string]string{"kardinal.io/pipeline": "nginx-demo", "kardinal.io/bundle": target.Name,
				"kardinal.io/environment": "prod"}},
		Spec:   kardinalv1alpha1.PromotionStepSpec{PipelineName: "nginx-demo", BundleName: target.Name, Environment: "prod"},
		Status: kardinalv1alpha1.PromotionStepStatus{State: "Verified"},
	}
	p := &kardinalv1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}},
			Holds: []kardinalv1alpha1.EnvironmentHold{{Environment: "prod", Bundle: holdRB, Reason: "INC-42",
				CreatedBy: "alice", Artifacts: lifecycle.ArtifactDigest(rb.Spec)}}}}
	return &holdFixture{pipeline: p, target: target, rb: rb, steps: []client.Object{step}}
}

// TestPolicyGateReconciler_HoldExemption (#1528): a blocking gate of the held
// environment passes for the rollback the hold names, never silently: the
// reason starts with EXEMPT and names the hold, who made it, why, and the
// gate's own result; the flip is a GateEvaluated AuditEvent; one
// GateExempted Warning Event is emitted per exempt episode; releasing the
// hold blocks the gate again. The exemption needs a rollback the controller
// verifies (#1528 QA): a gate of another environment, a Bundle that is not a
// rollback, one edited after the hold, one that restores an image never
// Verified in the environment or whose rollbackOf never was, and a hold
// without an artifact digest all block, and a refused exemption says why.
func TestPolicyGateReconciler_HoldExemption(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		edit       func(f *holdFixture)
		wantExempt bool
		wantRefuse string
	}{
		{name: "the verified held rollback passes", env: "prod", wantExempt: true},
		{name: "a gate of another environment is not exempt", env: "test"},
		{name: "no hold: no exemption", env: "prod", edit: func(f *holdFixture) { f.pipeline.Spec.Holds = nil }},
		{name: "not a rollback", env: "prod", edit: func(f *holdFixture) { f.rb.Labels = nil },
			wantRefuse: "is not a rollback Bundle"},
		{name: "edited after the hold", env: "prod",
			edit:       func(f *holdFixture) { f.rb.Spec.Images[0].Tag = "666" },
			wantRefuse: "no longer has the artifacts it had when the hold was made"},
		{name: "an image never Verified in the environment", env: "prod", edit: func(f *holdFixture) {
			f.rb.Spec.Images[0].Tag = "666"
			f.pipeline.Spec.Holds[0].Artifacts = lifecycle.ArtifactDigest(f.rb.Spec)
		}, wantRefuse: "image ghcr.io/org/app:666 was not deployed by a Bundle Verified in prod"},
		{name: "rollbackOf never Verified in the environment", env: "prod", edit: func(f *holdFixture) {
			f.steps[0].(*kardinalv1alpha1.PromotionStep).Status.State = "Failed"
		}, wantRefuse: "rollbackOf nginx-demo-v1 was never Verified in prod"},
		{name: "a hold without an artifact digest", env: "prod",
			edit:       func(f *holdFixture) { f.pipeline.Spec.Holds[0].Artifacts = "" },
			wantRefuse: "the hold records no artifact digest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newHoldFixture()
			if tc.edit != nil {
				tc.edit(f)
			}
			gate := makeGateInstance(tc.env+"-no-weekend", "default", holdRB, "!schedule.isWeekend", "5m")
			gate.Labels["kardinal.io/environment"] = tc.env
			gate.Labels["kardinal.io/gate-name"] = "no-weekend-deploys"
			objs := append([]client.Object{gate, f.rb, f.target, f.pipeline}, f.steps...)
			c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(objs...).WithStatusSubresource(gate).Build()
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
				if tc.wantRefuse != "" {
					assert.Contains(t, g.Status.Reason, "(hold exemption refused: ")
					assert.Contains(t, g.Status.Reason, tc.wantRefuse)
				} else {
					assert.NotContains(t, g.Status.Reason, "hold exemption refused")
				}
				return
			}
			assert.True(t, g.Status.Ready, "the held rollback passes the blocking gate")
			assert.True(t, strings.HasPrefix(g.Status.Reason, "EXEMPT: rollback "+holdRB+" holds prod (by alice: INC-42); without the hold: "),
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
			assert.True(t, strings.HasPrefix(exempted[0], "Warning GateExempted EXEMPT: rollback "+holdRB), exempted[0])

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

// TestHoldExemptible pins exactly which gates a hold can exempt
// (docs/policy-gates.md#rollback-hold-exemption): gate instances (they carry
// kardinal.io/bundle) of the held environment, except the freeze gate.
// Approval gates (#1510) must be added here as never exempt when they land.
func TestHoldExemptible(t *testing.T) {
	inst := func(env string, extra map[string]string) *kardinalv1alpha1.PolicyGate {
		g := makeGateInstance("g", "default", holdRB, "false", "5m")
		g.Labels["kardinal.io/environment"] = env
		for k, v := range extra {
			g.Labels[k] = v
		}
		return g
	}
	template := inst("prod", nil)
	delete(template.Labels, "kardinal.io/bundle")
	for name, tc := range map[string]struct {
		gate *kardinalv1alpha1.PolicyGate
		want bool
	}{
		"an instance of the held environment":  {inst("prod", nil), true},
		"an instance of another environment":   {inst("test", nil), false},
		"a template (no kardinal.io/bundle)":   {template, false},
		"the freeze gate of a paused Pipeline": {inst("prod", map[string]string{lifecycle.LabelFreeze: "true"}), false},
	} {
		assert.Equal(t, tc.want, policygate.HoldExemptible(tc.gate, "prod"), name)
	}
}
