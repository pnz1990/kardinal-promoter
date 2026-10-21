// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// unverifiedStamp is a version written while the AgentIndex was not Ready
// by oidc:fixer-bot (#2451).
func unverifiedStamp() *kardinalv1alpha1.Actor {
	return &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorUnverified, Principal: "oidc:fixer-bot"}
}

// readyIndex is the release's AgentIndex, Ready, naming users as agents.
func readyIndex(users map[string]kardinalv1alpha1.AgentIndexEntry) *kardinalv1alpha1.AgentIndex {
	return &kardinalv1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: indexName},
		Status: kardinalv1alpha1.AgentIndexStatus{Users: users,
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Built"}}}}
}

// TestActorContext_Unverified (#2451): while the AgentIndex is not Ready,
// stopping is always allowed and going needs verification, so an unverified
// version counts as an agent's for every restriction: actor.kind is agent
// (with no agent or sponsor), actor.unverified is true and actor.verified
// false. An actor.kind != "agent" gate (the generated agent gates among
// them) and an actor.verified gate hold it; actor.unverified is false for
// any other stamp.
// Covers ACTOR-INDEX-06.
func TestActorContext_Unverified(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stamp *kardinalv1alpha1.Actor
		expr  string
		want  bool
	}{
		{"an agent for restrictions", unverifiedStamp(),
			"actor.kind == 'agent' && actor.unverified && !actor.verified && actor.agent == '' && actor.principal == 'oidc:fixer-bot'", true},
		{"a not-an-agent gate holds it", unverifiedStamp(), "actor.kind != 'agent'", false},
		{"the agent gate holds it", unverifiedStamp(), "actor.kind != 'agent' || (agent.registered && !agent.suspended && agent.autonomy != 'deny')", false},
		{"a stamped-versions gate holds it", unverifiedStamp(), "actor.verified", false},
		{"a human-only gate holds it", unverifiedStamp(), "actor.kind == 'human'", false},
		{"a person's stamp is not unverified", &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "bob"},
			"!actor.unverified && actor.verified", true},
		{"an agent's stamp is not unverified", fixerStamp(), "!actor.unverified && actor.kind == 'agent'", true},
		{"no stamp is not unverified", nil, "!actor.unverified && !actor.verified", true},
	} {
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

// TestUnverifiedAuthor_Reverified (QA-2 on #2462): once the AgentIndex is
// Ready, an unverified version whose writer the index names as an agent
// (by user) is that agent's: the one-party rule refuses its sponsor's
// approval, and agent.* is the agent's. While the index is not Ready, or for
// a writer it does not name, the author stays unverified: still held by the
// agent gate, and the sponsor is not known.
// Covers ACTOR-INDEX-06.
func TestUnverifiedAuthor_Reverified(t *testing.T) {
	sponsorApproval := []kardinalv1alpha1.GateApproval{{Bundle: "app-v1", BundleUID: "uid-app-v1", Environment: "prod",
		User: "alice", Decision: "approve", Actor: &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "alice"}}}
	named := map[string]kardinalv1alpha1.AgentIndexEntry{"oidc:fixer-bot": {Agent: "fixer", Sponsor: "alice", Autonomy: "auto"}}
	identity := &kardinalv1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer", Generation: 1},
		Spec: kardinalv1alpha1.AgentIdentitySpec{Sponsor: kardinalv1alpha1.AgentSponsor{User: "alice"},
			Subjects: []kardinalv1alpha1.AgentSubject{{Kind: "User", Name: "oidc:fixer-bot"}}, Autonomy: "auto"},
		Status: kardinalv1alpha1.AgentIdentityStatus{Ready: true, ObservedGeneration: 1}}
	notReady := readyIndex(named)
	notReady.Status.Conditions[0] = metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "IndexFull"}
	for _, tc := range []struct {
		name     string
		objs     []client.Object
		expr     string
		approval bool
		ready    bool
		reason   string
	}{
		{"named once Ready: the sponsor's approval does not count", []client.Object{readyIndex(named), identity},
			"true", true, false, "sponsor"},
		{"not named (the Group-subject residual): the sponsor is not known, so the approval counts", []client.Object{readyIndex(nil), identity},
			"true", true, true, ""},
		{"named once Ready: agent.* is the agent's", []client.Object{readyIndex(named), identity},
			"actor.kind == 'agent' && actor.agent == 'fixer' && actor.sponsor == 'alice' && !actor.unverified && agent.registered", false, true, ""},
		{"not Ready: it stays unverified", []client.Object{notReady, identity},
			"actor.unverified && actor.agent == '' && !agent.registered", false, true, ""},
		{"not named: it stays unverified", []client.Object{readyIndex(nil), identity},
			"actor.unverified && actor.agent == ''", false, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := makeGateInstance("g", "default", "app-v1", tc.expr, "5m")
			gate.Labels["kardinal.io/environment"] = "prod"
			if tc.approval {
				gate.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}
				gate.Spec.Approvals = sponsorApproval
			}
			b := makeBundle("app-v1", "default")
			b.UID = "uid-app-v1"
			b.Spec.Actor = unverifiedStamp()
			got := evalGate(t, gate, b, tc.objs...)
			assert.Equal(t, tc.ready, got.Status.Ready, got.Status.Reason)
			if tc.reason != "" {
				assert.Contains(t, got.Status.Reason+fmtApprovals(got), tc.reason)
			}
		})
	}
}

// fmtApprovals is the reasons the gate's status gives its approvals.
func fmtApprovals(g *kardinalv1alpha1.PolicyGate) string {
	s := ""
	for _, a := range g.Status.Approvals {
		s += " " + a.User + ": " + a.Reason
	}
	return s
}

// TestUnverifiedOverrideNotHonoured (#2451): grants don't count from
// unverified. An override stamped unverified (written while the AgentIndex
// was not Ready) never opens a gate, and OverrideIgnored says why and what
// to do; the same override by a verified person does.
// Covers ACTOR-INDEX-06.
func TestUnverifiedOverrideNotHonoured(t *testing.T) {
	gate := makeGateInstance("g", "default", "app-v1", "false", "5m")
	created := metav1.NewTime(time.Date(2026, 10, 14, 9, 59, 0, 0, time.UTC))
	gate.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{{Reason: "incident 42", CreatedBy: "bob",
		CreatedAt: &created, ExpiresAt: metav1.NewTime(time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)),
		Actor: &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorUnverified, Principal: "bob"}}}
	got := evalGate(t, gate, makeBundle("app-v1", "default"))
	assert.False(t, got.Status.Ready, got.Status.Reason)
	assert.NotContains(t, got.Status.Reason, "OVERRIDDEN")
	c := meta.FindStatusCondition(got.Status.Conditions, "OverrideIgnored")
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Contains(t, c.Message, `"incident 42" by bob (unverified): written while kardinal's agent index was not Ready, `+
		"so it is not honoured; override again once the index is Ready")

	person := gate.DeepCopy()
	person.Spec.Overrides[0].Actor = &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "bob"}
	assert.True(t, evalGate(t, person, makeBundle("app-v1", "default")).Status.Ready, "a verified person's override opens it")
}

// TestAgentAutoGate_Unverified (#2451): going needs verification. A version
// stamped unverified is held by the kardinal-agents-auto gate (it has no
// registered agent), while the index is not Ready and after, unless the
// Ready index names its writer as a registered auto agent. A verified
// person's override opens it: the way to ship it without shipping it again.
// Covers ACTOR-INDEX-06.
func TestAgentAutoGate_Unverified(t *testing.T) {
	named := map[string]kardinalv1alpha1.AgentIndexEntry{"oidc:fixer-bot": {Agent: "fixer", Sponsor: "alice", Autonomy: "auto"}}
	identity := &kardinalv1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer", Generation: 1},
		Spec: kardinalv1alpha1.AgentIdentitySpec{Sponsor: kardinalv1alpha1.AgentSponsor{User: "alice"},
			Subjects: []kardinalv1alpha1.AgentSubject{{Kind: "User", Name: "oidc:fixer-bot"}}, Autonomy: "auto"},
		Status: kardinalv1alpha1.AgentIdentityStatus{Ready: true, ObservedGeneration: 1}}
	created := metav1.NewTime(time.Date(2026, 10, 14, 9, 59, 0, 0, time.UTC))
	override := kardinalv1alpha1.PolicyGateOverride{Reason: "shipped during IndexFull", CreatedBy: "bob", CreatedAt: &created,
		ExpiresAt: metav1.NewTime(time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)),
		Actor:     &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "bob"}}
	for _, tc := range []struct {
		name     string
		objs     []client.Object
		override bool
		ready    bool
	}{
		{"index not Ready: held", nil, false, false},
		{"index Ready, the writer is not named: held", []client.Object{readyIndex(nil), identity}, false, false},
		{"index Ready, the writer is a registered auto agent: passes", []client.Object{readyIndex(named), identity}, false, true},
		{"a verified person's override opens it", []client.Object{readyIndex(nil), identity}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := makeGateInstance("kardinal-agents-auto-default-test--app-v1", "default", "app-v1",
				"actor.kind != 'agent' || (agent.registered && !agent.suspended && agent.autonomy != 'deny')", "5m")
			if tc.override {
				gate.Spec.Overrides = []kardinalv1alpha1.PolicyGateOverride{override}
			}
			b := makeBundle("app-v1", "default")
			b.Spec.Actor = unverifiedStamp()
			got := evalGate(t, gate, b, tc.objs...)
			assert.Equal(t, tc.ready, got.Status.Ready, got.Status.Reason)
		})
	}
}
