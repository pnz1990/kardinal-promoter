// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// TestVerifyStamp (#2024 QA): the controller records an agent stamp only
// when the AgentIndex vouches for it; while the index is missing no agent
// stamp is recorded (agents fail closed) and people's and bots' stamps still
// are.
func TestVerifyStamp(t *testing.T) {
	const fx = "system:serviceaccount:ci:fixer"
	idx := &v1alpha1.AgentIndexStatus{
		Users:  map[string]v1alpha1.AgentIndexEntry{fx: {Agent: "fixer", Sponsor: "alice"}},
		Groups: map[string]v1alpha1.AgentIndexEntry{"oidc:triage": {Agent: "triage", Sponsor: "dave"}},
	}
	human := &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "bob"}
	agent := func(p, a, s string) *v1alpha1.Actor {
		return &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: p, Agent: a, Sponsor: s}
	}
	missing := errors.New("agent index kardinal-promoter does not exist")
	tests := []struct {
		name    string
		stamp   *v1alpha1.Actor
		by      string
		idx     *v1alpha1.AgentIndexStatus
		err     error
		kept    bool
		because string
	}{
		{"a person's stamp", human, "bob", idx, nil, true, ""},
		{"a person's stamp, index missing", human, "bob", nil, missing, true, ""},
		{"a person claiming agent, index missing (QA's case)", agent("bob", "fixer", "carol"), "bob", nil, missing, false, "could not be verified"},
		{"a person claiming agent", agent("bob", "fixer", "carol"), "bob", idx, nil, false, "no agent fixer of carol"},
		{"the agent", agent(fx, "fixer", "alice"), fx, idx, nil, true, ""},
		{"the agent, wrong sponsor", agent(fx, "fixer", "carol"), fx, idx, nil, false, "is agent fixer of alice"},
		{"the agent claiming human", &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: fx}, fx, idx, nil, false, "is agent fixer"},
		{"a group agent: membership cannot be confirmed (QA's probe)", agent("alice", "triage", "dave"), "alice", idx, nil, false,
			"registered by group"},
		{"a group agent's own member", agent("oidc:bot1", "triage", "dave"), "oidc:bot1", idx, nil, false, "registered by group"},
		{"the agent, wrong agent name", agent(fx, "other", "alice"), fx, idx, nil, false, "is agent fixer of alice"},
		{"someone else's stamp", human, "carol", idx, nil, false, "names bob"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, why := actor.VerifyStamp(tc.stamp, tc.by, tc.idx, tc.err)
			assert.Equal(t, tc.kept, got != nil)
			assert.Contains(t, why, tc.because)
		})
	}
}
