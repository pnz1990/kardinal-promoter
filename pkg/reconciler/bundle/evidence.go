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
	"sigs.k8s.io/controller-runtime/pkg/client"

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
	if loose := unpinned(b); len(loose) > 0 {
		setBundleCondition(b, condUpstreamEvidence, metav1.ConditionFalse, "NotPinned",
			fmt.Sprintf("%s not pinned by digest, so a verification upstream may have been of other bytes under the same tag; "+
				"this version is promoted through %s first", strings.Join(loose, ", "), strings.Join(upstream, ", ")))
		return nil
	}
	siblings, err := lifecycle.ListPipelineBundles(ctx, r.Client, b.Namespace, b.Spec.Pipeline)
	if err != nil {
		return fmt.Errorf("upstream evidence: %w", err)
	}
	starts, err := r.stepStarts(ctx, b)
	if err != nil {
		return err
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
		if e.ReplacedAt == nil {
			e.ReplacedAt = replacedAt(siblings, starts, b, env, e.VerifiedAt.Time)
		}
		e.SoakMinutes = evidenceSoak(e, now)
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
					consider(kardinalv1alpha1.UpstreamEvidence{Environment: env, Bundle: e.Bundle, VerifiedAt: e.VerifiedAt,
						ReplacedAt: e.ReplacedAt})
				}
			}
		}
	}
	return best, found
}

// unpinned lists b's artifacts that are not pinned by content: images
// without a digest, and a chart without one. A tag can be pushed again with
// other bytes, so a verification of the same tag may not be of these
// artifacts. A config commit is content-addressed.
func unpinned(b *kardinalv1alpha1.Bundle) []string {
	var out []string
	for _, img := range b.Spec.Images {
		if img.Digest == "" {
			out = append(out, img.Repository+":"+img.Tag)
		}
	}
	if c := b.Spec.Chart; c != nil && c.Version != "" && c.Digest == "" {
		out = append(out, "chart "+c.Name+" "+c.Version)
	}
	return out
}

// stepStarts lists, by environment, when each PromotionStep of b's Pipeline
// started (its creation), with the Bundle it promotes.
func (r *Reconciler) stepStarts(ctx context.Context, b *kardinalv1alpha1.Bundle) (map[string][]stepStart, error) {
	var steps kardinalv1alpha1.PromotionStepList
	if err := r.List(ctx, &steps, client.InNamespace(b.Namespace),
		client.MatchingLabels{"kardinal.io/pipeline": b.Spec.Pipeline}); err != nil {
		return nil, fmt.Errorf("upstream evidence: list promotion steps: %w", err)
	}
	out := map[string][]stepStart{}
	for _, ps := range steps.Items {
		out[ps.Spec.Environment] = append(out[ps.Spec.Environment],
			stepStart{bundle: ps.Spec.BundleName, at: ps.CreationTimestamp.Time})
	}
	return out, nil
}

// stepStart is when a PromotionStep of bundle started.
type stepStart struct {
	bundle string
	at     time.Time
}

// replacedAt is when other artifacts than b's first started promoting to
// env after verified, or nil: the earliest PromotionStep there, after
// verified, of a Bundle with other artifacts, or its Verified time there
// when its steps are gone (a retired Bundle). Bundles with the same
// artifacts as b, b among them, do not replace them.
func replacedAt(siblings []kardinalv1alpha1.Bundle, starts map[string][]stepStart, b *kardinalv1alpha1.Bundle,
	env string, verified time.Time) *metav1.Time {
	same := map[string]bool{b.Name: true}
	for i := range siblings {
		if lifecycle.SameArtifacts(&siblings[i], b) {
			same[siblings[i].Name] = true
		}
	}
	var first time.Time
	earlier := func(t time.Time) {
		if t.After(verified) && (first.IsZero() || t.Before(first)) {
			first = t
		}
	}
	for _, s := range starts[env] {
		if !same[s.bundle] {
			earlier(s.at)
		}
	}
	for i := range siblings {
		if same[siblings[i].Name] {
			continue
		}
		for _, st := range siblings[i].Status.Environments {
			if st.Name == env && st.HealthCheckedAt != nil {
				earlier(st.HealthCheckedAt.Time)
			}
		}
	}
	if first.IsZero() {
		return nil
	}
	t := metav1.NewTime(first)
	return &t
}

// evidenceSoak is how long e's artifacts ran in its environment: from its
// verification to ReplacedAt, or to now.
func evidenceSoak(e kardinalv1alpha1.UpstreamEvidence, now time.Time) int64 {
	if e.ReplacedAt != nil {
		now = e.ReplacedAt.Time
	}
	return soakSince(e.VerifiedAt.Time, now)
}

// refreshEvidence keeps the soak of b's carried verifications current: a
// verification whose artifacts other ones replaced since gets ReplacedAt, and
// stops counting. It reads Bundle and PromotionStep status only, and changes
// b in memory; the caller patches.
func (r *Reconciler) refreshEvidence(ctx context.Context, b *kardinalv1alpha1.Bundle, now time.Time) error {
	open := false
	for _, e := range b.Status.UpstreamEvidence {
		open = open || e.ReplacedAt == nil
	}
	if open {
		siblings, err := lifecycle.ListPipelineBundles(ctx, r.Client, b.Namespace, b.Spec.Pipeline)
		if err != nil {
			return fmt.Errorf("upstream evidence: %w", err)
		}
		starts, err := r.stepStarts(ctx, b)
		if err != nil {
			return err
		}
		for i := range b.Status.UpstreamEvidence {
			if e := &b.Status.UpstreamEvidence[i]; e.ReplacedAt == nil {
				e.ReplacedAt = replacedAt(siblings, starts, b, e.Environment, e.VerifiedAt.Time)
			}
		}
	}
	for i := range b.Status.UpstreamEvidence {
		e := &b.Status.UpstreamEvidence[i]
		e.SoakMinutes = evidenceSoak(*e, now)
	}
	return nil
}

// soakSince is the whole minutes from at to now, 0 when at is later.
func soakSince(at, now time.Time) int64 {
	if d := now.Sub(at); d > 0 {
		return int64(d.Minutes())
	}
	return 0
}
