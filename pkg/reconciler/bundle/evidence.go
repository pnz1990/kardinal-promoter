// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
)

// condUpstreamEvidence reports, for a Bundle with spec.intent.targetOnly,
// whether it carries another Bundle's verification of its artifacts in every
// environment upstream of its target (True, reason Carried or NoUpstream), so
// it promotes only the target, or promotes through them (False).
const condUpstreamEvidence = "UpstreamEvidence"

// decideUpstreamEvidence decides, once, whether the targetOnly Bundle b
// promotes only its target (#1311). Each environment upstream of the target
// (on the path to it, the target's fleet targets excluded) needs a Bundle of
// the Pipeline with the same artifacts (lifecycle.SameArtifacts), not
// rejected, whose promotion there was Verified: its status.environments
// entry, or a verification it carries itself. The newest is recorded in
// status.upstreamEvidence, and the Graph then has no upstream environment
// (graph.CarriedEnvironments). When one environment has none, or a verify
// gate on the target reads an upstream environment's verdicts, b promotes
// through them, and the condition says why. Changes b in memory only; the
// condition marks the decision, so a later reconcile keeps it.
func (r *Reconciler) decideUpstreamEvidence(ctx context.Context, log zerolog.Logger, b *kardinalv1alpha1.Bundle,
	pipeline *kardinalv1alpha1.Pipeline) error {
	if b.Spec.Intent == nil || !b.Spec.Intent.TargetOnly ||
		meta.FindStatusCondition(b.Status.Conditions, condUpstreamEvidence) != nil {
		return nil
	}
	target := b.Spec.Intent.TargetEnvironment
	upstream, err := upstreamOfTarget(pipeline, b)
	if err != nil {
		return err
	}
	if len(upstream) == 0 {
		setBundleCondition(b, condUpstreamEvidence, metav1.ConditionTrue, "NoUpstream",
			fmt.Sprintf("%s has no upstream environment", target))
		return nil
	}
	if gate, err := r.verifyGateOn(ctx, pipeline, b); err != nil {
		return err
	} else if gate != "" {
		setBundleCondition(b, condUpstreamEvidence, metav1.ConditionFalse, "VerifyGate",
			fmt.Sprintf("verify gate %s on %s reads the verdicts of an upstream environment, so this version is promoted through %s first",
				gate, target, strings.Join(upstream, ", ")))
		return nil
	}
	siblings, err := lifecycle.ListPipelineBundles(ctx, r.Client, b.Namespace, b.Spec.Pipeline)
	if err != nil {
		return fmt.Errorf("upstream evidence: %w", err)
	}
	now := r.now()
	var evidence []kardinalv1alpha1.UpstreamEvidence
	var missing, from []string
	for _, env := range upstream {
		e, ok := newestVerification(siblings, b, env)
		if !ok {
			missing = append(missing, env)
			continue
		}
		e.SoakMinutes = soakSince(e.VerifiedAt.Time, now)
		evidence = append(evidence, e)
		from = append(from, env+" by "+e.Bundle)
	}
	if len(missing) > 0 {
		b.Status.UpstreamEvidence = nil
		setBundleCondition(b, condUpstreamEvidence, metav1.ConditionFalse, "NotVerifiedUpstream",
			fmt.Sprintf("no other version of pipeline %s with these artifacts was verified in %s, so this version is promoted through %s first",
				b.Spec.Pipeline, strings.Join(missing, ", "), strings.Join(upstream, ", ")))
		return nil
	}
	b.Status.UpstreamEvidence = evidence
	setBundleCondition(b, condUpstreamEvidence, metav1.ConditionTrue, "Carried",
		fmt.Sprintf("promotes only %s; these artifacts were verified in %s", target, strings.Join(from, ", ")))
	log.Info().Strs("carried", upstream).Msg("targetOnly bundle carries the upstream verification")
	return nil
}

// upstreamOfTarget lists, in promotion order, the environments b's Graph
// would promote before its target without the evidence: the path to
// intent.targetEnvironment, without the target (for a fleet, its targets).
func upstreamOfTarget(pipeline *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) ([]string, error) {
	full := b.DeepCopy()
	full.Status.UpstreamEvidence = nil
	path, err := graph.PromotedEnvironments(pipeline, full)
	if err != nil {
		return nil, fmt.Errorf("upstream evidence: %w", err)
	}
	target := b.Spec.Intent.TargetEnvironment
	targets := []string{target}
	if byFleet, err := graph.FleetTargetEnvironments(pipeline); err == nil && len(byFleet[target]) > 0 {
		targets = byFleet[target]
	}
	return slices.DeleteFunc(path, func(e string) bool { return slices.Contains(targets, e) }), nil
}

// verifyGateOn returns the name of a verify gate (spec.verify) that applies
// to b's target, or "". Its verified environment is an upstream one, whose
// verdicts a Bundle that does not promote it never gets.
func (r *Reconciler) verifyGateOn(ctx context.Context, pipeline *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) (string, error) {
	gates, err := translator.CollectGates(ctx, r.Client, r.PolicyNamespaces, pipeline)
	if err != nil {
		return "", fmt.Errorf("upstream evidence: %w", err)
	}
	// A gate names an environment, or a fleet for all of its targets.
	target := b.Spec.Intent.TargetEnvironment
	targets := map[string]bool{target: true}
	if f := graph.FleetOf(pipeline, target); f != "" {
		targets[f] = true
	}
	if byFleet, err := graph.FleetTargetEnvironments(pipeline); err == nil {
		for _, t := range byFleet[target] {
			targets[t] = true
		}
	}
	for _, g := range gates {
		if g.Spec.Verify == nil {
			continue
		}
		for _, env := range strings.Split(g.Labels["kardinal.io/applies-to"], ",") {
			if targets[strings.TrimSpace(env)] {
				return g.Namespace + "/" + g.Name, nil
			}
		}
	}
	return "", nil
}

// newestVerification is the newest verification in env of b's artifacts by
// another Bundle of siblings that is not rejected: a Verified
// status.environments entry with its health check time, or one the sibling
// itself carries in status.upstreamEvidence (so the evidence outlives the
// Bundle that made it).
func newestVerification(siblings []kardinalv1alpha1.Bundle, b *kardinalv1alpha1.Bundle,
	env string) (kardinalv1alpha1.UpstreamEvidence, bool) {
	var best kardinalv1alpha1.UpstreamEvidence
	found := false
	consider := func(e kardinalv1alpha1.UpstreamEvidence) {
		if !found || e.VerifiedAt.After(best.VerifiedAt.Time) {
			best, found = e, true
		}
	}
	for i := range siblings {
		s := &siblings[i]
		if s.Name == b.Name || s.Spec.Rejected != nil || s.Status.Phase == phaseRejected ||
			!lifecycle.SameArtifacts(s, b) {
			continue
		}
		for _, st := range s.Status.Environments {
			if st.Name == env && st.Phase == "Verified" && st.HealthCheckedAt != nil {
				consider(kardinalv1alpha1.UpstreamEvidence{Environment: env, Bundle: s.Name, VerifiedAt: *st.HealthCheckedAt})
			}
		}
		if graph.CarriedEnvironments(s)[env] {
			for _, e := range s.Status.UpstreamEvidence {
				if e.Environment == env {
					consider(kardinalv1alpha1.UpstreamEvidence{Environment: env, Bundle: e.Bundle, VerifiedAt: e.VerifiedAt})
				}
			}
		}
	}
	return best, found
}

// updateEvidenceSoak keeps the soak of b's carried verifications current.
// Graph-first: a pure mutation before the caller's status patch.
func updateEvidenceSoak(b *kardinalv1alpha1.Bundle, now time.Time) {
	for i := range b.Status.UpstreamEvidence {
		e := &b.Status.UpstreamEvidence[i]
		e.SoakMinutes = soakSince(e.VerifiedAt.Time, now)
	}
}

// soakSince is the whole minutes from at to now, 0 when at is later.
func soakSince(at, now time.Time) int64 {
	if d := now.Sub(at); d > 0 {
		return int64(d.Minutes())
	}
	return 0
}
