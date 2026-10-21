// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// TestAgentLike (#2451): unverified counts as an agent for every
// restriction; a person, a bot, a controller and an unstamped version do
// not.
// Covers ACTOR-INDEX-06.
func TestAgentLike(t *testing.T) {
	for kind, want := range map[string]bool{
		v1alpha1.ActorAgent: true, v1alpha1.ActorUnverified: true,
		v1alpha1.ActorHuman: false, v1alpha1.ActorBot: false, v1alpha1.ActorController: false, v1alpha1.ActorUnknown: false, "": false,
	} {
		assert.Equal(t, want, actor.AgentLike(v1alpha1.Actor{Kind: kind, Principal: "p"}), kind)
	}
}

// TestReverify (QA-2 on #2462): an unverified stamp whose writer a Ready
// index names (by user) is that agent's, with its sponsor, so the one-party
// rule sees the sponsor; a writer it does not name, a writer named only
// through a group (the stamp records no groups: the documented residual),
// no index and any other stamp stay as they are.
// Covers ACTOR-INDEX-06.
func TestReverify(t *testing.T) {
	idx := &v1alpha1.AgentIndexStatus{
		Users:  map[string]v1alpha1.AgentIndexEntry{"oidc:fixer-bot": {Agent: "fixer", Sponsor: "alice", Autonomy: "auto"}},
		Groups: map[string]v1alpha1.AgentIndexEntry{"bots": {Agent: "tester", Sponsor: "carol"}},
	}
	unverified := func(p string) v1alpha1.Actor {
		return v1alpha1.Actor{Kind: v1alpha1.ActorUnverified, Principal: p, Via: "kardinal-cli"}
	}
	fixer := v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "oidc:fixer-bot", Agent: "fixer", Sponsor: "alice", Via: "kardinal-cli"}
	for _, tc := range []struct {
		name string
		in   v1alpha1.Actor
		idx  *v1alpha1.AgentIndexStatus
		want v1alpha1.Actor
	}{
		{"named by user: the agent's", unverified("oidc:fixer-bot"), idx, fixer},
		{"not named: unverified", unverified("bob"), idx, unverified("bob")},
		{"an agent of a group subject: unverified (the residual)", unverified("oidc:tester-bot"), &v1alpha1.AgentIndexStatus{
			Groups: idx.Groups}, unverified("oidc:tester-bot")},
		{"no index: unverified", unverified("oidc:fixer-bot"), nil, unverified("oidc:fixer-bot")},
		{"a person's stamp is kept", v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "oidc:fixer-bot"}, idx,
			v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "oidc:fixer-bot"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := actor.Reverify(tc.in, tc.idx)
			assert.Equal(t, tc.want, got)
		})
	}
	assert.True(t, actor.SameParty(actor.Reverify(unverified("oidc:fixer-bot"), idx),
		v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "alice"}), "the sponsor is one party with it")
}
