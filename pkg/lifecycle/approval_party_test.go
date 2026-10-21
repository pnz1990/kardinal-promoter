// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

func human(p string) *v1alpha1.Actor { return &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: p} }

func agentActor(name, sponsor string) *v1alpha1.Actor {
	return &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:" + name, Agent: name, Sponsor: sponsor}
}

func partyGate(p *v1alpha1.GateApprovalPolicy, decisions ...v1alpha1.GateApproval) *v1alpha1.PolicyGate {
	return &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "prod"}},
		Spec:       v1alpha1.PolicyGateSpec{Expression: "true", Approval: p, Approvals: decisions},
	}
}

func by(a *v1alpha1.Actor, d string) v1alpha1.GateApproval {
	user := ""
	if a != nil {
		user = a.Principal
	}
	return v1alpha1.GateApproval{Bundle: "app-v1", Environment: "prod", User: user, Groups: []string{}, Decision: d, Actor: a}
}

// TestTallyApprovals_OneParty: with excludeAuthor, an approver of the
// author's party does not count (docs/design/18-actor-policy.md section 4):
// the author, the author's agent, the author agent's sponsor, and another
// agent of that sponsor. Agents count only with the gate's opt-in, and never
// on their own party's work, whatever excludeAuthor says.
func TestTallyApprovals_OneParty(t *testing.T) {
	fixer := agentActor("fixer", "alice")     // alice's agent
	helper := agentActor("helper", "alice")   // alice's other agent
	reviewer := agentActor("reviewer", "bob") // bob's agent
	exclude := &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}
	agentsOK := &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true, AllowAgents: true,
		AllowedAgents: []string{"fixer", "helper", "reviewer"}}
	agentsOKNoExclude := &v1alpha1.GateApprovalPolicy{Required: 1, AllowAgents: true, AllowedAgents: []string{"fixer", "helper"}}
	cases := []struct {
		name     string
		author   v1alpha1.Actor
		policy   *v1alpha1.GateApprovalPolicy
		approver *v1alpha1.Actor
		counts   bool
		reason   string
	}{
		{"a person approves another's version", *human("alice"), exclude, human("bob"), true, ""},
		{"the author", *human("alice"), exclude, human("alice"), false, "the version's author"},
		{"the sponsor of the version's agent", *fixer, exclude, human("alice"), false, "the sponsor of the version's agent fixer"},
		{"another person approves an agent's version", *fixer, exclude, human("bob"), true, ""},
		{"an agent, gate not opted in", *human("carol"), exclude, reviewer, false, "approval.allowAgents"},
		{"an agent not in allowedAgents", *human("carol"),
			&v1alpha1.GateApprovalPolicy{Required: 1, AllowAgents: true, AllowedAgents: []string{"other"}}, reviewer, false, "not in approval.allowedAgents"},
		{"an opted-in agent of another party", *human("carol"), agentsOK, reviewer, true, ""},
		{"the author's own agent", *human("alice"), agentsOK, fixer, false, "the author's own agent fixer"},
		{"the version's agent approves itself", *fixer, agentsOK, fixer, false, "the version's author"},
		{"a sibling agent of the same sponsor", *fixer, agentsOK, helper, false, "same sponsor"},
		{"an agent of another sponsor approves an agent's version", *fixer, agentsOK, reviewer, true, ""},
		{"own party is refused without excludeAuthor too", *human("alice"), agentsOKNoExclude, fixer, false, "never counts on its own party's work"},
		{"without excludeAuthor the author counts (v0.11)", *human("alice"), &v1alpha1.GateApprovalPolicy{Required: 1}, human("alice"), true, ""},
		{"an unstamped approver against an unstamped author", actor.OrUnknown(nil, "alice"), exclude,
			&v1alpha1.Actor{Kind: v1alpha1.ActorUnknown, Principal: "alice"}, false, "the version's author"},
		{"an unstamped approval by the sponsor", *fixer, exclude, nil, false, "the sponsor of the version's agent fixer"},
		{"no policy: an agent never counts", *human("carol"), nil, reviewer, false, "approval.allowAgents"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := by(tc.approver, "approve")
			if tc.approver == nil {
				d = v1alpha1.GateApproval{Bundle: "app-v1", Environment: "prod", User: "alice", Groups: []string{}, Decision: "approve"}
			}
			got := TallyApprovals(partyGate(tc.policy, d), tc.author)
			require.Len(t, got.Records, 1)
			assert.Equal(t, tc.counts, got.Records[0].Counted, got.Records[0].Reason)
			if tc.reason != "" {
				assert.Contains(t, got.Records[0].Reason, tc.reason)
			}
		})
	}
}

// TestTallyApprovals_OneVotePerParty: required counts parties, not
// principals. An agent and its sponsor, or two agents of one sponsor, are
// one approval; the second is recorded as already counted.
func TestTallyApprovals_OneVotePerParty(t *testing.T) {
	policy := &v1alpha1.GateApprovalPolicy{Required: 2, ExcludeAuthor: true, AllowAgents: true,
		AllowedAgents: []string{"fixer", "helper", "reviewer"}}
	author := *human("carol")
	t.Run("an agent and its sponsor are one vote", func(t *testing.T) {
		got := TallyApprovals(partyGate(policy, by(human("alice"), "approve"), by(agentActor("fixer", "alice"), "approve")), author)
		assert.Len(t, got.Approvers, 1)
		assert.False(t, got.Met())
		var dup int
		for _, r := range got.Records {
			if !r.Counted {
				dup++
				assert.Contains(t, r.Reason, "one vote per party")
			}
		}
		assert.Equal(t, 1, dup)
	})
	t.Run("two agents of one sponsor are one vote", func(t *testing.T) {
		got := TallyApprovals(partyGate(policy, by(agentActor("fixer", "alice"), "approve"), by(agentActor("helper", "alice"), "approve")), author)
		assert.Len(t, got.Approvers, 1)
	})
	t.Run("two parties meet two", func(t *testing.T) {
		got := TallyApprovals(partyGate(policy, by(human("alice"), "approve"), by(agentActor("reviewer", "bob"), "approve")), author)
		assert.Len(t, got.Approvers, 2)
		assert.True(t, got.Met())
	})
	t.Run("a party's reject wins over its approve", func(t *testing.T) {
		got := TallyApprovals(partyGate(policy, by(human("alice"), "approve"), by(agentActor("fixer", "alice"), "reject"),
			by(human("dave"), "approve")), author)
		assert.Equal(t, []string{"dave"}, got.Approvers)
		assert.Len(t, got.Rejecters, 1)
		assert.False(t, got.Met())
	})
	t.Run("an ineligible agent's reject does not block", func(t *testing.T) {
		p := &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}
		got := TallyApprovals(partyGate(p, by(human("dave"), "approve"), by(agentActor("reviewer", "bob"), "reject")), author)
		assert.Empty(t, got.Rejecters)
		assert.True(t, got.Met())
	})
}

// TestTallyApprovals_ControllerAuthorHeld: with excludeAuthor, a version a
// kardinal component created (a controller stamp) holds the gate, as a
// component creator did in v0.11.
func TestTallyApprovals_ControllerAuthorHeld(t *testing.T) {
	author := v1alpha1.Actor{Kind: v1alpha1.ActorController, Principal: "system:serviceaccount:kardinal-system:kardinal-promoter",
		Via: v1alpha1.ViaSubscription}
	got := TallyApprovals(partyGate(&v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}, by(human("bob"), "approve")), author)
	assert.Contains(t, got.Held, "kardinal component")
	assert.False(t, got.Met())
}

// TestApprovalStandingOf_Parties: the CLI's and the UI's standing follows the
// same rules: a sponsor whose agent already approved is told so, an agent
// caller of the author's party may not approve, and a waived gate
// (status.approvalRequired false) is not waiting on anyone.
func TestApprovalStandingOf_Parties(t *testing.T) {
	policy := &v1alpha1.GateApprovalPolicy{Required: 2, AllowAgents: true, AllowedAgents: []string{"fixer", "reviewer"}}
	gate := partyGate(policy, by(agentActor("fixer", "alice"), "approve"))
	st := ApprovalStandingOf(gate, *human("carol"), &ApproverIdentity{User: "alice"})
	assert.Equal(t, ApprovalCounted, st.Block)
	assert.Contains(t, st.Reason, "one vote per party")
	assert.Contains(t, st.Who, "agent fixer")

	own := ApprovalStandingOf(partyGate(policy), *human("alice"), &ApproverIdentity{User: "system:serviceaccount:ci:fixer", Actor: agentActor("fixer", "alice")})
	assert.Equal(t, ApprovalOwnParty, own.Block)
	assert.False(t, own.CanApprove)

	waived := partyGate(policy)
	f := false
	waived.Status.ApprovalRequired = &f
	assert.True(t, ApprovalStandingOf(waived, *human("carol"), nil).Waived)
	assert.True(t, ApprovalWaived(waived))
}

// TestBundleAuthor: the stamp, or {unknown, created-by}.
func TestBundleAuthor(t *testing.T) {
	assert.Equal(t, v1alpha1.Actor{Kind: v1alpha1.ActorUnknown}, BundleAuthor(nil))
	b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{AnnotationCreatedBy: "alice"}}}
	assert.Equal(t, v1alpha1.Actor{Kind: v1alpha1.ActorUnknown, Principal: "alice"}, BundleAuthor(b))
	b.Spec.Actor = agentActor("fixer", "alice")
	assert.Equal(t, *b.Spec.Actor, BundleAuthor(b))
}

// TestDecisions_Unverified (#2451): an approval or a verdict stamped
// unverified (written while the AgentIndex was not Ready) never counts, as
// a person's or an agent's: its writer may be an agent the index names by
// User or Group. The reason says so, and what to do. The same person's
// decision stamped human counts.
// Covers ACTOR-INDEX-06.
func TestDecisions_Unverified(t *testing.T) {
	unverified := &v1alpha1.Actor{Kind: v1alpha1.ActorUnverified, Principal: "bob"}
	author := v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "alice"}
	for name, p := range map[string]*v1alpha1.GateApprovalPolicy{
		"no policy":    nil,
		"anyone":       {Required: 1},
		"bob is named": {Required: 1, AllowedUsers: []string{"bob"}, ExcludeAuthor: true},
		"agents too":   {Required: 1, AllowAgents: true, AllowedAgents: []string{"fixer"}},
	} {
		ok, why := ApprovalDecisionCounts(p, Approver{User: "bob", Actor: unverified}, author)
		assert.False(t, ok, name)
		assert.Contains(t, why, "unverified: written while kardinal's agent index was not Ready", name)
		assert.Contains(t, why, "approve again", name)
		ok, _ = ApprovalDecisionCounts(p, Approver{User: "bob", Actor: human("bob")}, author)
		assert.True(t, ok, "%s: the same person, stamped human", name)
	}
	for name, p := range map[string]*v1alpha1.GateVerifyPolicy{
		"anyone":       {Check: "qa", Required: 1},
		"bob is named": {Check: "qa", Required: 1, AllowedUsers: []string{"bob"}},
	} {
		ok, why := VerdictWriterAllowed(p, *unverified, nil)
		assert.False(t, ok, name)
		assert.Contains(t, why, "bob is unverified", name)
		ok, _ = VerdictWriterAllowed(p, *human("bob"), nil)
		assert.True(t, ok, "%s: the same person, stamped human", name)
	}
}
