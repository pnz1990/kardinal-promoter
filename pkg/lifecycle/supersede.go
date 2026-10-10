// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// SupersedingSiblings returns the Bundles among siblings (the Bundles of b's
// Pipeline) that supersede b, newest last in the order of siblings:
//
//   - of b's type: image Bundles are superseded only by image Bundles, and so
//     on (a mixed Bundle only by a mixed one);
//   - neither rejected nor carrying a rejected artifact (rejected, from the
//     Bundles of the Pipeline): such a Bundle never promotes;
//   - not Superseded, Failed or Rejected; a Verified one only with
//     countVerified;
//   - created after b (CompareCreation);
//   - not the rollback of one fleet target (TargetRollback) other than b's
//     own, or, with env set, other than env: the rollback of one target
//     does not stop the fleet's rollout to the other targets (D1), but it
//     supersedes the fleet's promotion of that target.
//
// None does when p holds an environment on b (spec.holds, kardinal rollback
// --hold): the hold pins the environment to b until it is released. p may be
// nil (no holds).
//
// The Bundle reconciler supersedes b with it (env ""), and the PromotionStep
// push guard (#1603) refuses a push to env only when one of these, for env,
// already made it, so a promotion is refused exactly when it is superseded
// there.
func SupersedingSiblings(p *v1alpha1.Pipeline, b *v1alpha1.Bundle, siblings []v1alpha1.Bundle,
	rejected *RejectedArtifacts, countVerified bool, env string) []*v1alpha1.Bundle {
	if p != nil && HoldNaming(p, b.Name) != nil {
		return nil
	}
	var out []*v1alpha1.Bundle
	for i := range siblings {
		s := &siblings[i]
		if s.Name == b.Name || s.Spec.Pipeline != b.Spec.Pipeline || s.Spec.Type != b.Spec.Type {
			continue
		}
		if Rejected(s) {
			continue
		}
		if rejected != nil {
			if _, bad := rejected.Carries(s); bad {
				continue
			}
		}
		if t := TargetRollback(p, s); t != "" {
			if (env == "" && t != TargetRollback(p, b)) || (env != "" && t != env) {
				continue
			}
		}
		switch s.Status.Phase {
		case "Superseded", "Failed", "Rejected":
			continue
		case "Verified":
			if !countVerified {
				continue
			}
		}
		if CompareCreation(s, b) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// TargetRollback is the fleet target b rolls back when b is the rollback of
// one fleet target (label kardinal.io/rollback, intent.targetEnvironment a
// target of a fleet of p), else "". p may be nil.
func TargetRollback(p *v1alpha1.Pipeline, b *v1alpha1.Bundle) string {
	if p == nil || b.Labels[LabelRollback] != "true" || b.Spec.Intent == nil || !graph.HasFleets(p) {
		return ""
	}
	if t := b.Spec.Intent.TargetEnvironment; t != "" && graph.FleetOf(p, t) != "" {
		return t
	}
	return ""
}
