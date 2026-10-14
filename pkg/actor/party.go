// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor

import (
	"slices"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// The one-party rule (docs/design/17-actors.md, section 10.3): an agent and
// its sponsor count as one party, so neither verifies or approves the
// other's work, and two agents of one sponsor are one vote.

// Party returns the identities that count as one with a: the principal, and
// for an agent also its sponsor and the agent itself.
func Party(a v1alpha1.Actor) []string {
	var out []string
	if a.Principal != "" {
		out = append(out, a.Principal)
	}
	if a.Kind == v1alpha1.ActorAgent {
		out = append(out, a.Sponsor, "agent:"+a.Agent)
	}
	return out
}

// SameParty reports whether a and b count as one party: an agent and its
// sponsor, two agents of one sponsor, or one principal.
func SameParty(a, b v1alpha1.Actor) bool {
	pb := Party(b)
	return slices.ContainsFunc(Party(a), func(x string) bool { return slices.Contains(pb, x) })
}

// PartyKey is the one vote a counts for: its sponsor for an agent, else its
// principal.
func PartyKey(a v1alpha1.Actor) string {
	if a.Kind == v1alpha1.ActorAgent {
		return a.Sponsor
	}
	return a.Principal
}
