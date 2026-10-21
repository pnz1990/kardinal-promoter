// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
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

// verdictTrail is the gate's Verdict* AuditEvents in the order of the trail
// (lifecycle.CompareAuditEvents), as "action uid" with their names.
func verdictTrail(t *testing.T, c client.Client) (trail, names []string) {
	t.Helper()
	var list kardinalv1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("default")))
	items := list.Items
	sort.Slice(items, func(a, b int) bool { return lifecycle.CompareAuditEvents(&items[a], &items[b]) < 0 })
	for _, e := range items {
		if strings.HasPrefix(e.Spec.Action, "Verdict") {
			trail = append(trail, e.Spec.Action+" "+e.Spec.BundleUID)
			names = append(names, e.Name)
		}
	}
	return trail, names
}

// TestPolicyGateReconciler_VerdictReentry (#2380): a Verdict that leaves a
// gate's verdicts and comes back (its AgentRun deleted and created again)
// is recorded again, and revoked again when it leaves again: the trail
// shows the state, not only the first appearance. Each record has a name of
// its own (an appearance of the Verdict, status.verdictAppearances), and a
// second reconcile of one state writes nothing new.
func TestPolicyGateReconciler_VerdictReentry(t *testing.T) {
	gate := makeGateInstance("verify", "default", "nginx-demo-v1", "true", "5m")
	gate.Labels["kardinal.io/pipeline"] = "nginx-demo"
	gate.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Environment: "uat", Required: 1,
		AllowedAgents: []string{"qa"}}
	verdict := agentVerdict("r1", "qa", "bob", "pass", 1, 1, 0)
	gate.Spec.Verdicts = []kardinalv1alpha1.GateVerdict{verdict}
	bundle := makeBundle("nginx-demo-v1", "default")
	bundle.UID = "uid"
	bundle.Status.Phase = "Promoting"
	bundle.Annotations = map[string]string{"kardinal.io/created-by": "alice"}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).WithStatusSubresource(gate, bundle).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	now := verifyNow
	r.NowFn = func() time.Time { return now }
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "verify", Namespace: "default"}}
	step := func(verdicts []kardinalv1alpha1.GateVerdict) {
		t.Helper()
		now = now.Add(time.Minute)
		var cur kardinalv1alpha1.PolicyGate
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &cur))
		cur.Spec.Verdicts = verdicts
		require.NoError(t, c.Update(context.Background(), &cur))
		for range 2 { // twice: idempotent
			_, err := r.Reconcile(context.Background(), req)
			require.NoError(t, err)
		}
	}
	step([]kardinalv1alpha1.GateVerdict{verdict})
	step(nil)
	step([]kardinalv1alpha1.GateVerdict{verdict})
	step(nil)
	trail, names := verdictTrail(t, c)
	assert.Equal(t, []string{"VerdictRecorded uid", "VerdictRevoked uid", "VerdictRecorded uid", "VerdictRevoked uid"}, trail)
	assert.Len(t, slices.Compact(slices.Sorted(slices.Values(names))), 4, "four names: %v", names)
	var cur kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &cur))
	assert.Equal(t, map[string]int32{"r1": 2}, cur.Status.VerdictAppearances, "kept after it left")

	// It comes back a third time: recorded, and the gate counts it.
	step([]kardinalv1alpha1.GateVerdict{verdict})
	trail, _ = verdictTrail(t, c)
	assert.Equal(t, "VerdictRecorded uid", trail[len(trail)-1], "the trail ends on the state: recorded")
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &cur))
	assert.True(t, cur.Status.Ready, cur.Status.Reason)
}

// TestPolicyGateReconciler_VerdictRecordsPerBundle (#2380, QA-5 on #2377): a
// Bundle deleted and created again under its name has a gate and a Verdict
// of the same names; its VerdictRecorded is a record of its own, beside the
// earlier Bundle's, and names its UID.
func TestPolicyGateReconciler_VerdictRecordsPerBundle(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme()).
		WithStatusSubresource(&kardinalv1alpha1.PolicyGate{}, &kardinalv1alpha1.Bundle{}).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return verifyNow }
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "verify", Namespace: "default"}}
	for _, uid := range []string{"uid-a", "uid-b"} {
		gate := makeGateInstance("verify", "default", "nginx-demo-v1", "true", "5m")
		gate.Labels["kardinal.io/pipeline"] = "nginx-demo"
		gate.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Environment: "uat", Required: 1,
			AllowedAgents: []string{"qa"}}
		v := agentVerdict("r1", "qa", "bob", "pass", 1, 1, 0)
		v.BundleUID = uid
		gate.Spec.Verdicts = []kardinalv1alpha1.GateVerdict{v}
		bundle := makeBundle("nginx-demo-v1", "default")
		bundle.UID = types.UID(uid)
		bundle.Status.Phase = "Promoting"
		bundle.Annotations = map[string]string{"kardinal.io/created-by": "alice"}
		require.NoError(t, c.Create(context.Background(), bundle))
		require.NoError(t, c.Create(context.Background(), gate))
		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		// The Bundle is deleted, and its gate with it; its AuditEvents stay.
		require.NoError(t, c.Delete(context.Background(), gate))
		require.NoError(t, c.Delete(context.Background(), bundle))
	}
	trail, names := verdictTrail(t, c)
	assert.ElementsMatch(t, []string{"VerdictRecorded uid-a", "VerdictRecorded uid-b"}, trail)
	assert.NotEqual(t, names[0], names[1])
}

// graphOf is Bundle b's kro Graph, controlled by b, whose
// status.managedResources lists gates (name and UID; an empty UID is an
// entry kro recorded before it created the object). It sets b's graphRef.
func graphOf(b *kardinalv1alpha1.Bundle, owner types.UID, gates ...[2]string) *unstructured.Unstructured {
	g := &unstructured.Unstructured{}
	g.SetGroupVersionKind(graph.GraphGVK)
	g.SetNamespace(b.Namespace)
	g.SetName("graph-" + b.Name)
	ctl := true
	g.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: kardinalv1alpha1.GroupVersion.String(), Kind: "Bundle",
		Name: b.Name, UID: owner, Controller: &ctl}})
	var managed []interface{}
	for _, gate := range gates {
		managed = append(managed, map[string]interface{}{"nodeID": "PolicyGates", "apiVersion": "kardinal.io/v1alpha1",
			"kind": "PolicyGate", "namespace": b.Namespace, "name": gate[0], "uid": gate[1]})
	}
	_ = unstructured.SetNestedSlice(g.Object, managed, "status", "managedResources")
	b.Status.GraphRef = g.GetName()
	return g
}

// TestPolicyGateReconciler_InBundleGraph (#2380, QA-5 on #2395): a Bundle
// has no finalizer, so deleting it frees its name while its gates are still
// being deleted, and a Bundle of the same name can exist while an earlier
// one's gate is reconciled. A gate kro applied is evaluated only while its
// Bundle's current Graph (status.graphRef, controlled by the Bundle) lists
// it, whatever the two creation times say (several API servers, a
// restore). An earlier Bundle's gate writes no record, says why on its
// InBundleGraph condition, and is looked at again in seconds; the answer is
// kept in status.bundleUID, so the Graph is read once.
func TestPolicyGateReconciler_InBundleGraph(t *testing.T) {
	created := metav1.NewTime(verifyNow.Add(-time.Hour))
	cases := []struct {
		name        string
		gateCreated time.Duration // after the Bundle
		graph       func(b *kardinalv1alpha1.Bundle) *unstructured.Unstructured
		cached      bool
		evaluated   bool
		reason, why string
		graphReads  int
	}{
		{name: "listed in its Bundle's Graph", gateCreated: time.Second,
			graph: func(b *kardinalv1alpha1.Bundle) *unstructured.Unstructured {
				return graphOf(b, b.UID, [2]string{"verify", "gate-uid"})
			}, evaluated: true, reason: "Listed", graphReads: 1},
		{name: "created in the second its Bundle was", gateCreated: 0,
			graph: func(b *kardinalv1alpha1.Bundle) *unstructured.Unstructured {
				return graphOf(b, b.UID, [2]string{"verify", "gate-uid"})
			}, evaluated: true, reason: "Listed", graphReads: 1},
		{name: "older than its Bundle, adopted by its Graph (a restore, a clock ahead)", gateCreated: -time.Hour,
			graph: func(b *kardinalv1alpha1.Bundle) *unstructured.Unstructured {
				return graphOf(b, b.UID, [2]string{"verify", "gate-uid"})
			}, evaluated: true, reason: "Listed", graphReads: 1},
		{name: "recorded by kro before it had a UID", gateCreated: time.Second,
			graph: func(b *kardinalv1alpha1.Bundle) *unstructured.Unstructured {
				return graphOf(b, b.UID, [2]string{"verify", ""})
			}, evaluated: true, reason: "Listed", graphReads: 1},
		{name: "checked before: the Graph is not read again", gateCreated: time.Second, cached: true,
			evaluated: true},
		{name: "an earlier Bundle's gate: the Graph lists another of its name", gateCreated: time.Second,
			graph: func(b *kardinalv1alpha1.Bundle) *unstructured.Unstructured {
				return graphOf(b, b.UID, [2]string{"verify", "another-uid"})
			}, reason: "NotListed", why: "does not list this gate: it is an earlier Bundle's", graphReads: 2},
		{name: "the Graph lists another kind of the gate's name", gateCreated: time.Second,
			graph: func(b *kardinalv1alpha1.Bundle) *unstructured.Unstructured {
				g := graphOf(b, b.UID)
				_ = unstructured.SetNestedSlice(g.Object, []interface{}{map[string]interface{}{"nodeID": "PRStatuses",
					"apiVersion": "kardinal.io/v1alpha1", "kind": "PRStatus", "namespace": "default", "name": "verify", "uid": "gate-uid"}},
					"status", "managedResources")
				return g
			}, reason: "NotListed", why: "does not list this gate", graphReads: 2},
		{name: "no Graph yet", gateCreated: time.Second,
			reason: "GraphPending", why: "has no Graph yet"},
		{name: "the Graph of the name is still an earlier Bundle's", gateCreated: time.Second,
			graph: func(b *kardinalv1alpha1.Bundle) *unstructured.Unstructured {
				return graphOf(b, "uid-earlier", [2]string{"verify", "gate-uid"})
			}, reason: "GraphPending", why: "is not yet Bundle", graphReads: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := makeBundle("nginx-demo-v1", "default")
			bundle.UID, bundle.CreationTimestamp = "uid-b", created
			bundle.Status.Phase = "Promoting"
			bundle.Annotations = map[string]string{"kardinal.io/created-by": "alice"}
			gate := makeGateInstance("verify", "default", "nginx-demo-v1", "true", "5m")
			gate.UID = "gate-uid"
			gate.Labels["kro.run/node-id"] = "PolicyGates"
			gate.CreationTimestamp = metav1.NewTime(created.Add(tc.gateCreated))
			if tc.cached {
				gate.Status.BundleUID = "uid-b"
			}
			objs := []client.Object{gate}
			if tc.graph != nil {
				objs = append(objs, tc.graph(bundle))
			}
			objs = append(objs, bundle)
			reads := 0
			c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(objs...).
				WithStatusSubresource(&kardinalv1alpha1.PolicyGate{}, &kardinalv1alpha1.Bundle{}).
				WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
					obj client.Object, opts ...client.GetOption) error {
					if u, ok := obj.(*unstructured.Unstructured); ok && u.GroupVersionKind() == graph.GraphGVK {
						reads++
					}
					return cl.Get(ctx, key, obj, opts...)
				}}).Build()
			r, err := policygate.NewReconciler(c)
			require.NoError(t, err)
			r.NowFn = func() time.Time { return verifyNow }
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "verify", Namespace: "default"}}
			var versions []string
			for range 2 { // twice: the Graph is read once, the skip writes once
				res, err := r.Reconcile(context.Background(), req)
				require.NoError(t, err)
				if !tc.evaluated {
					assert.Equal(t, 10*time.Second, res.RequeueAfter, "looked at again shortly")
				}
				var g kardinalv1alpha1.PolicyGate
				require.NoError(t, c.Get(context.Background(), req.NamespacedName, &g))
				versions = append(versions, g.ResourceVersion)
			}
			if !tc.evaluated {
				assert.Equal(t, versions[0], versions[1], "the condition is written once, not at every look")
			}
			var got kardinalv1alpha1.PolicyGate
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			var events kardinalv1alpha1.AuditEventList
			require.NoError(t, c.List(context.Background(), &events, client.InNamespace("default")))
			assert.Equal(t, tc.graphReads, reads, "Graph reads")
			cond := meta.FindStatusCondition(got.Status.Conditions, "InBundleGraph")
			if tc.evaluated {
				assert.NotNil(t, got.Status.LastEvaluatedAt, "evaluated")
				assert.Equal(t, "uid-b", got.Status.BundleUID)
				assert.NotEmpty(t, events.Items, "it records")
				for _, e := range events.Items {
					assert.Equal(t, "uid-b", e.Spec.BundleUID, e.Name)
				}
				if tc.reason != "" {
					require.NotNil(t, cond)
					assert.Equal(t, metav1.ConditionTrue, cond.Status)
					assert.Equal(t, tc.reason, cond.Reason)
				}
				return
			}
			assert.Nil(t, got.Status.LastEvaluatedAt, "not evaluated")
			assert.Empty(t, events.Items, "no record names the Bundle for a gate not its own")
			assert.Empty(t, got.Status.BundleUID)
			require.NotNil(t, cond, "the gate says why")
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, tc.reason, cond.Reason)
			assert.Contains(t, cond.Message, tc.why)
		})
	}
}

// TestPolicyGateReconciler_EarlierBundlesGate (QA-5 on #2395): the probe.
// Bundle uid-a's gate records; the Bundle is deleted and created again
// (uid-b) under its name while the gate is still there; the gate, uid-a's,
// writes nothing that names uid-b.
func TestPolicyGateReconciler_EarlierBundlesGate(t *testing.T) {
	created := metav1.NewTime(verifyNow.Add(-time.Hour))
	gate := makeGateInstance("verify", "default", "nginx-demo-v1", "true", "5m")
	gate.Labels["kardinal.io/pipeline"] = "nginx-demo"
	gate.Labels["kro.run/node-id"] = "PolicyGates"
	gate.UID = "gate-a"
	gate.CreationTimestamp = metav1.NewTime(created.Add(time.Second))
	gate.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Environment: "uat", Required: 1,
		AllowedAgents: []string{"qa"}}
	v := agentVerdict("r1", "qa", "bob", "pass", 1, 1, 0)
	v.BundleUID = "uid-a"
	gate.Spec.Verdicts = []kardinalv1alpha1.GateVerdict{v}
	earlier := makeBundle("nginx-demo-v1", "default")
	earlier.UID, earlier.CreationTimestamp = "uid-a", created
	earlier.Status.Phase = "Promoting"
	earlier.Annotations = map[string]string{"kardinal.io/created-by": "alice"}
	earlierGraph := graphOf(earlier, earlier.UID, [2]string{"verify", "gate-a"})
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, earlier, earlierGraph).
		WithStatusSubresource(&kardinalv1alpha1.PolicyGate{}, &kardinalv1alpha1.Bundle{}).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return verifyNow }
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "verify", Namespace: "default"}}
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	before, _ := verdictTrail(t, c)
	require.Equal(t, []string{"VerdictRecorded uid-a"}, before)

	// The Bundle is deleted and created again under its name; the earlier
	// one's gate (and Graph, being deleted) are still there.
	require.NoError(t, c.Delete(context.Background(), earlier))
	again := makeBundle("nginx-demo-v1", "default")
	again.UID, again.CreationTimestamp = "uid-b", metav1.NewTime(created.Add(10*time.Second))
	again.Status.Phase = "Promoting"
	again.Annotations = map[string]string{"kardinal.io/created-by": "alice"}
	require.NoError(t, c.Create(context.Background(), again))
	for range 2 {
		_, err = r.Reconcile(context.Background(), req)
		require.NoError(t, err)
	}
	var list kardinalv1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("default")))
	for _, e := range list.Items {
		assert.NotEqual(t, "uid-b", e.Spec.BundleUID, "%s %s names the new Bundle for the earlier one's gate", e.Spec.Action, e.Name)
	}
	var cur kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &cur))
	cond := meta.FindStatusCondition(cur.Status.Conditions, "InBundleGraph")
	require.NotNil(t, cond)
	assert.Equal(t, "GraphPending", cond.Reason, cond.Message)
}

// TestPolicyGateReconciler_SettledBundleRetiredGraph: a settled Bundle's
// gates are not evaluated, and its retired Graph (deleted) lists none: such
// a gate, still being deleted, gets no InBundleGraph condition, no requeue
// and no write.
func TestPolicyGateReconciler_SettledBundleRetiredGraph(t *testing.T) {
	bundle := makeBundle("nginx-demo-v1", "default")
	bundle.Status.Phase = "Superseded"
	bundle.Status.GraphRef = "graph-nginx-demo-v1" // retired: the Graph is gone
	gate := makeGateInstance("verify", "default", "nginx-demo-v1", "true", "5m")
	gate.UID = "gate-uid"
	gate.Labels["kro.run/node-id"] = "PolicyGates"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, bundle).
		WithStatusSubresource(&kardinalv1alpha1.PolicyGate{}, &kardinalv1alpha1.Bundle{}).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "verify", Namespace: "default"}}
	var before kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &before))
	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Equal(t, before.ResourceVersion, got.ResourceVersion, "nothing written")
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, "InBundleGraph"))
}
