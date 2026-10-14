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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

var verifyNow = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)

func agentVerdict(name, agent, sponsor, result string, run, attempt int, at time.Duration) kardinalv1alpha1.GateVerdict {
	ts := metav1.NewTime(verifyNow.Add(at))
	principal := "system:serviceaccount:default:" + agent
	v := kardinalv1alpha1.GateVerdict{Name: name, CreatedAt: &ts, Bundle: "nginx-demo-v1", BundleUID: "uid",
		Environment: "uat", Check: "explore", Result: result, Summary: name, By: principal,
		Actor: &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorAgent, Principal: principal, Agent: agent, Sponsor: sponsor}}
	if run > 0 {
		v.Run = &kardinalv1alpha1.VerdictRun{Name: name, Index: run, Attempt: attempt}
	}
	return v
}

// TestPolicyGateReconciler_Verify evaluates verify gates through the
// reconciler: the reason line, the quorum, a fail, the one-party rule, the
// timeout and its requeue, and status.verdicts. Each case is reconciled
// twice (idempotent: the second reconcile writes nothing new).
func TestPolicyGateReconciler_Verify(t *testing.T) {
	base := kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Environment: "uat", Required: 2,
		AllowedAgents: []string{"qa", "fixer"}}
	healthAt := metav1.NewTime(verifyNow.Add(-30 * time.Minute))
	cases := []struct {
		name       string
		policy     func(p *kardinalv1alpha1.GateVerifyPolicy)
		verdicts   []kardinalv1alpha1.GateVerdict
		author     *kardinalv1alpha1.Actor
		expr       string
		wantReady  bool
		wantReason string
		requeueMax time.Duration
	}{
		{name: "waiting with one pass",
			verdicts:   []kardinalv1alpha1.GateVerdict{agentVerdict("r1", "qa", "bob", "pass", 1, 1, 0)},
			wantReason: "waiting for verdicts: 1 of 2 (qa)"},
		{name: "two runs pass",
			verdicts: []kardinalv1alpha1.GateVerdict{agentVerdict("r1", "qa", "bob", "pass", 1, 1, 0),
				agentVerdict("r2", "qa", "bob", "pass", 2, 1, time.Second)},
			wantReady: true, wantReason: "verified: 2 of 2 (qa ×2)"},
		{name: "a fail blocks",
			verdicts: []kardinalv1alpha1.GateVerdict{agentVerdict("r1", "qa", "bob", "pass", 1, 1, 0),
				agentVerdict("r2", "qa", "bob", "fail", 2, 1, time.Second)},
			wantReason: "verification failed: qa: r2"},
		{name: "the author agent does not verify its own version",
			author: &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorAgent, Principal: "system:serviceaccount:default:fixer",
				Agent: "fixer", Sponsor: "alice"},
			verdicts: []kardinalv1alpha1.GateVerdict{agentVerdict("f1", "fixer", "alice", "pass", 1, 1, 0),
				agentVerdict("f2", "fixer", "alice", "pass", 2, 1, time.Second)},
			wantReason: "waiting for verdicts: 0 of 2"},
		{name: "the expression still decides", expr: "false",
			verdicts: []kardinalv1alpha1.GateVerdict{agentVerdict("r1", "qa", "bob", "pass", 1, 1, 0),
				agentVerdict("r2", "qa", "bob", "pass", 2, 1, time.Second)},
			wantReason: "= false"},
		{name: "verdicts.* in the expression", expr: "verdicts.passed >= 1 && verdicts.findings.high == 0",
			policy:    func(p *kardinalv1alpha1.GateVerifyPolicy) { p.Required = 1 },
			verdicts:  []kardinalv1alpha1.GateVerdict{agentVerdict("r1", "qa", "bob", "pass", 1, 1, 0)},
			wantReady: true, wantReason: "verified: 1 of 1 (qa)"},
		{name: "waiting requeues at the deadline",
			policy:     func(p *kardinalv1alpha1.GateVerifyPolicy) { p.Timeout = "31m" },
			wantReason: "waiting for verdicts: 0 of 2", requeueMax: 2 * time.Minute},
		{name: "timed out, fail",
			policy:     func(p *kardinalv1alpha1.GateVerifyPolicy) { p.Timeout = "10m" },
			verdicts:   []kardinalv1alpha1.GateVerdict{agentVerdict("r1", "qa", "bob", "pass", 1, 1, -25*time.Minute)},
			wantReason: "verification timed out after 10m: 1 of 2 (qa)"},
		{name: "timed out, allow",
			policy:    func(p *kardinalv1alpha1.GateVerifyPolicy) { p.Timeout = "10m"; p.OnTimeout = "allow" },
			wantReady: true, wantReason: "verification timed out after 10m, allowed by onTimeout: 0 of 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expr := tc.expr
			if expr == "" {
				expr = "true"
			}
			p := base
			if tc.policy != nil {
				tc.policy(&p)
			}
			gate := makeGateInstance("verify", "default", "nginx-demo-v1", expr, "5m")
			gate.Spec.Verify = &p
			gate.Spec.Verdicts = tc.verdicts
			bundle := makeBundle("nginx-demo-v1", "default")
			bundle.UID = "uid"
			bundle.Status.Phase = "Promoting"
			bundle.Annotations = map[string]string{"kardinal.io/created-by": "alice"}
			bundle.Spec.Actor = tc.author
			bundle.Status.Environments = []kardinalv1alpha1.EnvironmentStatus{{Name: "uat", Phase: "Verified", HealthCheckedAt: &healthAt}}
			c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return verifyNow }
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "verify", Namespace: "default"}}
			var first kardinalv1alpha1.PolicyGate
			for i := range 2 {
				res, err := r.Reconcile(context.Background(), req)
				require.NoError(t, err)
				var got kardinalv1alpha1.PolicyGate
				require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
				assert.Equal(t, tc.wantReady, got.Status.Ready, got.Status.Reason)
				assert.Contains(t, got.Status.Reason, tc.wantReason)
				if tc.requeueMax > 0 {
					assert.LessOrEqual(t, res.RequeueAfter, tc.requeueMax)
				}
				assert.Len(t, got.Status.Verdicts, len(tc.verdicts))
				if i == 0 {
					first = got
					continue
				}
				assert.Equal(t, first.Status.Verdicts, got.Status.Verdicts, "status.verdicts is stable")
				assert.Equal(t, first.ResourceVersion, got.ResourceVersion, "a second reconcile writes nothing")
			}
		})
	}
}

// TestPolicyGateReconciler_VerdictAudit: each verdict that appears in, or
// leaves, a verify gate's spec.verdicts writes one VerdictRecorded or
// VerdictRevoked AuditEvent with the verdict's actor.
func TestPolicyGateReconciler_VerdictAudit(t *testing.T) {
	gate := makeGateInstance("verify", "default", "nginx-demo-v1", "true", "5m")
	gate.Labels["kardinal.io/pipeline"] = "nginx-demo"
	gate.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Environment: "uat", Required: 1,
		AllowedAgents: []string{"qa"}}
	gate.Spec.Verdicts = []kardinalv1alpha1.GateVerdict{agentVerdict("r1", "qa", "bob", "pass", 1, 1, 0)}
	bundle := makeBundle("nginx-demo-v1", "default")
	bundle.UID = "uid"
	bundle.Status.Phase = "Promoting"
	bundle.Annotations = map[string]string{"kardinal.io/created-by": "alice"}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return verifyNow }
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "verify", Namespace: "default"}}
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	actions := func() map[string][]kardinalv1alpha1.AuditEvent {
		var list kardinalv1alpha1.AuditEventList
		require.NoError(t, c.List(context.Background(), &list, client.InNamespace("default")))
		out := map[string][]kardinalv1alpha1.AuditEvent{}
		for _, e := range list.Items {
			if strings.HasPrefix(e.Spec.Action, "Verdict") {
				out[e.Spec.Action] = append(out[e.Spec.Action], e)
			}
		}
		return out
	}
	got := actions()
	require.Len(t, got["VerdictRecorded"], 1, "recorded once over two reconciles")
	require.NotNil(t, got["VerdictRecorded"][0].Spec.Actor)
	assert.Equal(t, "qa", got["VerdictRecorded"][0].Spec.Actor.Agent)
	assert.Contains(t, got["VerdictRecorded"][0].Spec.Message, "agent qa for bob")

	// The Verdict is revoked: the Graph drops it from spec.verdicts.
	var cur kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &cur))
	cur.Spec.Verdicts = nil
	require.NoError(t, c.Update(context.Background(), &cur))
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	got = actions()
	assert.Len(t, got["VerdictRevoked"], 1)
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &cur))
	assert.False(t, cur.Status.Ready)
	assert.Empty(t, cur.Status.Verdicts)
}
