// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor

import (
	"fmt"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// VerifyStamp is the stamp an AuditEvent may record for a change made by
// by, and why a stamp is not recorded (empty when it is). Admission checks a
// stamp's principal, via and kind always, but its agent and sponsor only
// while the AgentIndex exists, so the controller checks again before it
// records one: a stamp naming someone other than by is dropped, an agent
// stamp is recorded only when the index (idx, nil when idxErr says why it
// is missing or not ready) says by is that agent of that sponsor (for an
// agent registered by group, which the controller cannot see, that the
// agent is registered with that sponsor), and a non-agent stamp of a user
// the index names as an agent is dropped. Agents fail closed: while the
// index is missing, no agent stamp is recorded.
func VerifyStamp(stamp *v1alpha1.Actor, by string, idx *v1alpha1.AgentIndexStatus, idxErr error) (*v1alpha1.Actor, string) {
	if stamp == nil {
		return nil, ""
	}
	if stamp.Principal != by {
		return nil, fmt.Sprintf("its stamp names %s, not %s", stamp.Principal, by)
	}
	if idx == nil {
		if stamp.Kind != v1alpha1.ActorAgent {
			return stamp, ""
		}
		why := "the AgentIndex is missing"
		if idxErr != nil {
			why = idxErr.Error()
		}
		return nil, "its agent stamp could not be verified: " + why
	}
	e, isAgent := idx.Users[by]
	switch {
	case stamp.Kind != v1alpha1.ActorAgent && isAgent:
		return nil, fmt.Sprintf("its stamp says %s, but the AgentIndex says %s is agent %s", stamp.Kind, by, e.Agent)
	case stamp.Kind != v1alpha1.ActorAgent:
		return stamp, ""
	case isAgent:
		if e.Agent != stamp.Agent || e.Sponsor != stamp.Sponsor {
			return nil, fmt.Sprintf("its stamp names agent %s of %s, but the AgentIndex says %s is agent %s of %s",
				stamp.Agent, stamp.Sponsor, by, e.Agent, e.Sponsor)
		}
		return stamp, ""
	}
	for _, g := range idx.Groups {
		if g.Agent == stamp.Agent && g.Sponsor == stamp.Sponsor {
			return stamp, ""
		}
	}
	return nil, fmt.Sprintf("the AgentIndex has no agent %s of %s for %s", stamp.Agent, stamp.Sponsor, by)
}
