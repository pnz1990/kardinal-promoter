// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

func TestSamePartyAndKey(t *testing.T) {
	alice := v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "alice"}
	bob := v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "bob"}
	fixer := v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "sa:fixer", Agent: "fixer", Sponsor: "alice"}
	tester := v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "sa:tester", Agent: "tester", Sponsor: "alice"}
	qa := v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "sa:qa", Agent: "qa", Sponsor: "bob"}
	unknown := v1alpha1.Actor{Kind: v1alpha1.ActorUnknown}
	cases := []struct {
		a, b v1alpha1.Actor
		same bool
	}{
		{alice, alice, true},
		{alice, bob, false},
		{alice, fixer, true},  // an agent and its sponsor
		{fixer, alice, true},  // symmetric
		{fixer, tester, true}, // two agents of one sponsor
		{fixer, qa, false},
		{bob, qa, true},
		{unknown, alice, false},
		{unknown, unknown, false}, // no principal: nothing to compare
	}
	for _, tc := range cases {
		assert.Equal(t, tc.same, actor.SameParty(tc.a, tc.b), "%+v %+v", tc.a, tc.b)
	}
	assert.Equal(t, "alice", actor.PartyKey(fixer))
	assert.Equal(t, "alice", actor.PartyKey(alice))
	assert.Equal(t, "sa:x", actor.PartyKey(v1alpha1.Actor{Kind: v1alpha1.ActorBot, Principal: "sa:x"}))
}
