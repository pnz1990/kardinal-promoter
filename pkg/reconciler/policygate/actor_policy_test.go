// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"errors"
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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

const indexName = "kardinal-promoter"

func fixerStamp() *kardinalv1alpha1.Actor {
	return &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:fixer",
		Agent: "fixer", Sponsor: "alice", Asserted: &kardinalv1alpha1.AssertedActor{Model: "claude-opus-5"}}
}

// registry is the AgentIdentity fixer (status ready unless notReady) and
// the release's AgentIndex with its entry (blocked when suspended).
func registry(autonomy string, suspended, notReady bool) []client.Object {
	id := &kardinalv1alpha1.AgentIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: "fixer", Generation: 1},
		Spec: kardinalv1alpha1.AgentIdentitySpec{Sponsor: kardinalv1alpha1.AgentSponsor{User: "alice"},
			Subjects: []kardinalv1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: "ci", Name: "fixer"}},
			Autonomy: autonomy, Suspended: suspended},
		Status: kardinalv1alpha1.AgentIdentityStatus{Ready: !notReady, ObservedGeneration: 1},
	}
	entry := kardinalv1alpha1.AgentIndexEntry{Agent: "fixer", Sponsor: "alice", Autonomy: autonomy}
	if suspended {
		entry.Blocked = "AgentIdentity fixer is suspended"
	}
	if notReady {
		entry = kardinalv1alpha1.AgentIndexEntry{Agent: "fixer", Sponsor: "alice", Blocked: "AgentIdentity fixer is not ready"}
	}
	idx := &kardinalv1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: indexName},
		Status: kardinalv1alpha1.AgentIndexStatus{Users: map[string]kardinalv1alpha1.AgentIndexEntry{
			"system:serviceaccount:ci:fixer": entry},
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Built"}}}}
	return []client.Object{id, idx}
}

// compactRegistry is registry with the AgentIndex in the compact format
// (#2071), as the controller writes it once the actor policies read it.
func compactRegistry(autonomy string, suspended, notReady bool) []client.Object {
	objs := registry(autonomy, suspended, notReady)
	idx := objs[1].(*kardinalv1alpha1.AgentIndex)
	enc, _ := actor.EncodeEntry(idx.Status.Users["system:serviceaccount:ci:fixer"])
	idx.Status = kardinalv1alpha1.AgentIndexStatus{
		Users:        map[string]kardinalv1alpha1.AgentIndexEntry{},
		Groups:       map[string]kardinalv1alpha1.AgentIndexEntry{actor.LegacyReaderGroup: actor.LegacyReaderGuard},
		CompactUsers: map[string]string{"system:serviceaccount:ci:fixer": enc},
		Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: actor.ReasonCompactFormat},
			{Type: actor.ConditionCompactReady, Status: metav1.ConditionTrue, Reason: "Built"}}}
	return objs
}

// evalGate reconciles gate (twice: idempotent) against bundle and objs and
// returns it.
func evalGate(t *testing.T, gate *kardinalv1alpha1.PolicyGate, bundle *kardinalv1alpha1.Bundle,
	objs ...client.Object) *kardinalv1alpha1.PolicyGate {
	t.Helper()
	bundle.Status.Phase = "Promoting"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(append(objs, gate, bundle)...).
		WithStatusSubresource(gate, bundle).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.AgentIndexName = indexName
	r.NowFn = func() time.Time { return time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC) }
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: gate.Name, Namespace: gate.Namespace}}
	var got kardinalv1alpha1.PolicyGate
	var first kardinalv1alpha1.PolicyGateStatus
	for i := range 2 {
		_, err = r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
		if i == 0 {
			first = *got.Status.DeepCopy()
			continue
		}
		assert.Equal(t, first.Ready, got.Status.Ready, "idempotent")
		assert.Equal(t, first.Reason, got.Status.Reason, "idempotent")
		assert.Equal(t, first.ApprovalRequired, got.Status.ApprovalRequired, "idempotent")
	}
	return &got
}

// TestActorContext: actor.* is the Bundle's stamp, or {unknown,
// created-by} without one; every key is present.
func TestActorContext(t *testing.T) {
	cases := []struct {
		name  string
		stamp *kardinalv1alpha1.Actor
		expr  string
		want  bool
	}{
		{"agent kind", fixerStamp(), "actor.kind == 'agent' && actor.agent == 'fixer' && actor.sponsor == 'alice'", true},
		{"verified", fixerStamp(), "actor.verified && actor.principal == 'system:serviceaccount:ci:fixer'", true},
		{"no asserted text reaches a gate", fixerStamp(), "!('asserted' in actor)", true},
		{"human", &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "bob", Via: "ui"},
			"actor.kind == 'human' && actor.via == 'ui' && actor.agent == ''", true},
		{"no stamp reads unknown with created-by", nil, "actor.kind == 'unknown' && actor.principal == 'ci-bot' && !actor.verified", true},
		{"require a stamp holds an unstamped version", nil, "actor.verified", false},
		{"bundle.createdBy stays an alias", nil, "bundle.createdBy == actor.principal", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := makeGateInstance("g", "default", "app-v1", tc.expr, "5m")
			b := makeBundle("app-v1", "default")
			b.Annotations = map[string]string{"kardinal.io/created-by": "ci-bot"}
			b.Spec.Actor = tc.stamp
			got := evalGate(t, gate, b)
			assert.Equal(t, tc.want, got.Status.Ready, got.Status.Reason)
		})
	}
}

// TestAgentContext: agent.* comes from the registry's status (the
// AgentIdentity's status.ready and the AgentIndex entry), live.
func TestAgentContext(t *testing.T) {
	cases := []struct {
		name     string
		stamp    *kardinalv1alpha1.Actor
		registry []client.Object
		expr     string
		want     bool
	}{
		{"registered", fixerStamp(), registry("auto", false, false),
			"agent.registered && agent.name == 'fixer' && agent.sponsor == 'alice' && agent.autonomy == 'auto' && !agent.suspended", true},
		{"suspended", fixerStamp(), registry("auto", true, false), "agent.registered && agent.suspended", true},
		{"not ready is not registered", fixerStamp(), registry("auto", false, true), "!agent.registered && agent.autonomy == ''", true},
		{"deleted is not registered", fixerStamp(), nil, "!agent.registered && agent.name == 'fixer'", true},
		{"a person's version", &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "bob"},
			registry("auto", false, false), "!agent.registered && agent.name == '' && !agent.suspended", true},
		// The compact index format (#2071) is read the same way.
		{"registered, compact index", fixerStamp(), compactRegistry("auto", false, false),
			"agent.registered && agent.name == 'fixer' && agent.sponsor == 'alice' && agent.autonomy == 'auto' && !agent.suspended", true},
		{"suspended, compact index", fixerStamp(), compactRegistry("auto", true, false), "agent.registered && agent.suspended", true},
		{"an index not built registers no one", fixerStamp(), func() []client.Object {
			objs := registry("auto", false, false)
			objs[1].(*kardinalv1alpha1.AgentIndex).Status.Conditions = nil
			return objs
		}(), "!agent.registered", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := makeGateInstance("g", "default", "app-v1", tc.expr, "5m")
			b := makeBundle("app-v1", "default")
			b.Spec.Actor = tc.stamp
			got := evalGate(t, gate, b, tc.registry...)
			assert.Equal(t, tc.want, got.Status.Ready, got.Status.Reason)
		})
	}
}

// TestAgentContext_ReadErrorFailsClosed: an AgentIdentity read error other
// than NotFound blocks the gate.
func TestAgentContext_ReadErrorFailsClosed(t *testing.T) {
	gate := makeGateInstance("g", "default", "app-v1", "actor.kind != 'agent' || agent.registered", "5m")
	b := makeBundle("app-v1", "default")
	b.Spec.Actor = fixerStamp()
	b.Status.Phase = "Promoting"
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(gate, b).WithStatusSubresource(gate, b).
		WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
			obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*kardinalv1alpha1.AgentIdentity); ok {
				return errors.New("etcd down")
			}
			return cl.Get(ctx, key, obj, opts...)
		}}).Build()
	r, err := policygate.NewReconciler(c)
	require.NoError(t, err)
	r.AgentIndexName = indexName
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "g", Namespace: "default"}})
	require.NoError(t, err)
	var got kardinalv1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "g", Namespace: "default"}, &got))
	assert.False(t, got.Status.Ready)
	assert.Contains(t, got.Status.Reason, "etcd down")
}

// TestApprovalRequiredWhen: the policy holds the gate only while
// requiredWhen is true; status.approvalRequired records it; approvals.met
// says so to the expression; an error fails closed.
func TestApprovalRequiredWhen(t *testing.T) {
	policy := func(when string) *kardinalv1alpha1.GateApprovalPolicy {
		return &kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true, RequiredWhen: when}
	}
	cases := []struct {
		name         string
		stamp        *kardinalv1alpha1.Actor
		expr         string
		when         string
		approvals    []kardinalv1alpha1.GateApproval
		wantReady    bool
		wantReason   string
		wantRequired *bool
	}{
		{name: "waived for a person", stamp: &kardinalv1alpha1.Actor{Kind: "human", Principal: "bob"}, expr: "true",
			when: "actor.kind == 'agent'", wantReady: true, wantReason: "approval not required", wantRequired: ptr(false)},
		{name: "required for an agent", stamp: fixerStamp(), expr: "true", when: "actor.kind == 'agent'",
			wantReason: "waiting for approvals: 0 of 1", wantRequired: ptr(true)},
		{name: "met by another party", stamp: fixerStamp(), expr: "true", when: "actor.kind == 'agent'",
			approvals: []kardinalv1alpha1.GateApproval{{Bundle: "app-v1", Environment: "prod", User: "bob", Groups: []string{}, Decision: "approve"}},
			wantReady: true, wantReason: "approved by bob", wantRequired: ptr(true)},
		{name: "the sponsor cannot meet it", stamp: fixerStamp(), expr: "true", when: "actor.kind == 'agent'",
			approvals:  []kardinalv1alpha1.GateApproval{{Bundle: "app-v1", Environment: "prod", User: "alice", Groups: []string{}, Decision: "approve"}},
			wantReason: "waiting for approvals: 0 of 1", wantRequired: ptr(true)},
		{name: "approvals.met is true while waived", stamp: &kardinalv1alpha1.Actor{Kind: "human", Principal: "bob"},
			expr: "approvals.met", when: "false", wantReady: true, wantRequired: ptr(false)},
		{name: "approvals.met is false while required and unmet", stamp: fixerStamp(), expr: "approvals.met", when: "true",
			wantReason: "= false", wantRequired: ptr(true)},
		{name: "an error fails closed", stamp: fixerStamp(), expr: "true", when: "actor.nope == 1", wantReason: "approval.requiredWhen"},
		{name: "unset is always", stamp: &kardinalv1alpha1.Actor{Kind: "human", Principal: "bob"}, expr: "true",
			wantReason: "waiting for approvals", wantRequired: ptr(true)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := makeGateInstance("g", "default", "app-v1", tc.expr, "5m")
			gate.Spec.Approval = policy(tc.when)
			gate.Spec.Approvals = tc.approvals
			b := makeBundle("app-v1", "default")
			b.Spec.Actor = tc.stamp
			got := evalGate(t, gate, b)
			assert.Equal(t, tc.wantReady, got.Status.Ready, got.Status.Reason)
			assert.Contains(t, got.Status.Reason, tc.wantReason)
			assert.Equal(t, tc.wantRequired, got.Status.ApprovalRequired)
		})
	}
}

// TestAgentOverrideNotHonoured: an override whose actor is an agent never
// opens a gate; it is named in the OverrideIgnored condition and audited
// as not honoured.
func TestAgentOverrideNotHonoured(t *testing.T) {
	gate := makeGateInstance("g", "default", "app-v1", "false", "5m")
	created := metav1.NewTime(time.Date(2026, 10, 14, 9, 59, 0, 0, time.UTC))
	gate.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{{Reason: "let me through", CreatedBy: "system:serviceaccount:ci:fixer",
		CreatedAt: &created, ExpiresAt: metav1.NewTime(time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)), Actor: fixerStamp()}}
	b := makeBundle("app-v1", "default")
	got := evalGate(t, gate, b)
	assert.False(t, got.Status.Ready, got.Status.Reason)
	assert.NotContains(t, got.Status.Reason, "OVERRIDDEN")
	var ignored bool
	for _, c := range got.Status.Conditions {
		if c.Type == "OverrideIgnored" && c.Status == metav1.ConditionTrue {
			ignored = true
			assert.Contains(t, c.Message, "overrides by agents are not honoured")
		}
	}
	assert.True(t, ignored)

	person := gate.DeepCopy()
	person.Spec.Overrides[0].Actor = &kardinalv1alpha1.Actor{Kind: "human", Principal: "bob"}
	person.Spec.Overrides[0].CreatedBy = "bob"
	assert.True(t, evalGate(t, person, makeBundle("app-v1", "default")).Status.Ready, "a person's override opens it")
}

// TestAgentAutoGate: the kardinal-agents-auto gate (pkg/graph/agents.go,
// pinned by TestBuilder_AgentGates) on a registered agent's version. It
// passes at autonomy auto; at approve (the AgentIdentity default) it waits
// for a person and says so, not that the agent is unregistered: spec.message
// explains a false expression only (#2017, found as TestCLI_MCP's test
// version held in test with "not registered" on a registered agent).
// Suspended, deny and unregistered agents fail the expression and lead with
// the message.
func TestAgentAutoGate(t *testing.T) {
	const message = "the version's agent is not registered, suspended, or its AgentIdentity autonomy is deny"
	cases := []struct {
		name         string
		registry     []client.Object
		wantReady    bool
		wantPrefix   string
		wantRequired bool
	}{
		{"auto passes", registry("auto", false, false), true, "actor.kind != 'agent'", false},
		{"approve waits for a person", registry("approve", false, false), false, "waiting for approvals: 0 of 1", true},
		{"suspended", registry("auto", true, false), false, message + " (", false},
		{"deny", registry("deny", false, false), false, message + " (", false},
		{"not registered", nil, false, message + " (", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := makeGateInstance("kardinal-agents-auto-default-test--app-v1", "default", "app-v1",
				"actor.kind != 'agent' || (agent.registered && !agent.suspended && agent.autonomy != 'deny')", "5m")
			gate.Spec.Message = message
			gate.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true,
				RequiredWhen: "actor.kind == 'agent' && agent.autonomy == 'approve'"}
			b := makeBundle("app-v1", "default")
			b.Spec.Actor = fixerStamp()
			got := evalGate(t, gate, b, tc.registry...)
			assert.Equal(t, tc.wantReady, got.Status.Ready, got.Status.Reason)
			assert.True(t, strings.HasPrefix(got.Status.Reason, tc.wantPrefix), got.Status.Reason)
			if !strings.HasPrefix(tc.wantPrefix, message) {
				assert.NotContains(t, got.Status.Reason, message, "the message explains a false expression only")
			}
			require.NotNil(t, got.Status.ApprovalRequired)
			assert.Equal(t, tc.wantRequired, *got.Status.ApprovalRequired)
		})
	}
}

func ptr(b bool) *bool { return &b }
