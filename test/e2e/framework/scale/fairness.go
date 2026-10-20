// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"context"
	"fmt"
	"sort"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestScale_TenantFairness* time tenant B's Bundles twice in one run: alone,
// before tenant A's load starts, and under it, and judge them against B's
// own time alone and A's own steps, on the same controller and host, not by
// whether B ends before A: on a fast host A's load ends before a fair B can
// (#2252). How much slower B is under A's load than alone depends on the
// host as much as on the queue (a GitHub runner's CPU, saturated by A, slows
// B's own work 8 to 13 times), so no bound on that tells a fair queue from
// an unfair one. Two things do (the tables on #2252 and #2257):
//   - with a queue that holds B until A drains, B's first environment is
//     Verified only after A's load ended (1 to 3 s after, in every run);
//   - without the fair queue (controller-runtime's default queue), B's steps
//     wait their turn behind A's backlog like A's own. With
//     TestScale_TenantFairnessManyRepos at 60 repositories on a GitHub
//     runner, B's slowest step took 1.2 to 1.38 times A's median step
//     without it, and 0.62 with it. At ci's 30 repositories the two overlap
//     (up to 1.0 with the fair queue, 1.26 without), so the ci profile does
//     not judge it (Profile.TenantBacklogRatio) and its absolute
//     TenantStepP99 is what fails the default queue there.
// On a host where A's load builds no backlog (A's steps run about as fast
// as B's alone, as on 32 cores at ci's sizes), there is no queue to be
// unfair in, and only starvation is judged. Every verdict lists the checks
// it judged and the ones it did not, with why (Fairness.Checks).

// BacklogFactor is how many times longer than B's slowest step alone A's
// median step must take for A's load to count as a backlog: then A's steps
// wait in the queue, and a fair queue serves B's ahead of them. On 32 cores
// A's median step took 2 to 3 s against B's 1 to 2 s alone (no backlog); on
// a GitHub runner 11 to 59 s against 1 to 2 s.
const BacklogFactor = 3

// FairnessSlack is added to the starvation window and to the bound on B's
// steps: the objects' timestamps are whole seconds, so a duration between
// two of them is up to 2 s off.
const FairnessSlack = 2 * time.Second

// TenantBundle is one of tenant B's Bundles, timed from its objects: its
// creation, its first environment Verified (zero if none was) and its last
// (zero unless the Bundle ended Verified), and its slowest step, from the
// step's creation to its Verified condition.
type TenantBundle struct {
	Name                 string
	Created, First, Last time.Time
	SlowestStep          time.Duration
}

// Latency is the Bundle's time from its creation to its last environment
// Verified (the latency-slo invariant's Bundle end to end).
func (b TenantBundle) Latency() time.Duration { return b.Last.Sub(b.Created) }

// TenantLoad is tenant A's load, timed from its objects: when its last step
// was Verified, and its steps' median time from creation to Verified.
type TenantLoad struct {
	End        time.Time
	StepMedian time.Duration
}

// BacklogCheck is how JudgeFairness judges B's steps against A's backlog:
// Ratio is the most B's slowest step may take as a fraction of A's median
// step (Profile.TenantBacklogRatio); 0 leaves it unjudged, for Why.
type BacklogCheck struct {
	Ratio float64
	Why   string
}

// Fairness is the verdict on tenant B's Bundles under A's load.
type Fairness struct {
	// Alone is the slowest of B's Bundles alone, AloneStep the slowest of
	// their steps; Worst is the slowest B Bundle under A's load, and
	// SlowestStep the slowest of its steps.
	Alone, AloneStep, Worst, SlowestStep time.Duration
	// Judged counts the B Bundles A's load outlasted long enough to tell
	// whether they waited for all of it; Backlog is whether B's steps were
	// judged against A's backlog.
	Judged  int
	Backlog bool
	// Checks says, one line each, which checks were judged and which were
	// not, and why: a check that was skipped is never silent.
	Checks     []string
	Violations []string
}

// JudgeFairness judges tenant B's Bundles under tenant A's load (loaded)
// against B's Bundles alone (alone) and A's load (a):
//   - starvation: a loaded Bundle that A's load outlasted by more than B's
//     slowest Bundle alone, plus FairnessSlack, has an environment Verified
//     before A's load ended (the timestamps are whole seconds: the same
//     second is not before). Its first environment is a third of the work
//     B did alone in that time; one with none Verified waited for all of
//     A's load, however short A's load was;
//   - served ahead of A's backlog, when bc.Ratio is set and A's median step
//     took more than BacklogFactor times B's slowest step alone: no step of
//     a loaded Bundle takes longer than bc.Ratio times A's median step,
//     plus FairnessSlack;
//   - contention was measured: every loaded Bundle was created before A's
//     load ended and ended Verified, and at least one could be judged for
//     starvation.
func JudgeFairness(alone, loaded []TenantBundle, a TenantLoad, bc BacklogCheck) Fairness {
	var f Fairness
	if len(alone) == 0 {
		f.Violations = append(f.Violations, "no Bundle of tenant B ran alone: no baseline to compare with")
		return f
	}
	for _, b := range alone {
		if b.Last.IsZero() {
			f.Violations = append(f.Violations, fmt.Sprintf("tenant B's Bundle %s, alone, did not end Verified: no baseline to compare with", b.Name))
			return f
		}
		f.Alone = max(f.Alone, b.Latency())
		f.AloneStep = max(f.AloneStep, b.SlowestStep)
	}
	window := f.Alone + FairnessSlack
	bound := time.Duration(bc.Ratio*float64(a.StepMedian)) + FairnessSlack
	switch {
	case bc.Ratio == 0:
		f.Checks = append(f.Checks, "B's steps against A's backlog: not judged: "+bc.Why)
	case a.StepMedian <= BacklogFactor*f.AloneStep:
		f.Checks = append(f.Checks, fmt.Sprintf("B's steps against A's backlog: not judged: A's median step (%s) is not more than %d times B's slowest step alone (%s), so A's load built no backlog",
			a.StepMedian, BacklogFactor, f.AloneStep))
	default:
		f.Backlog = true
	}
	late := 0
	for _, b := range loaded {
		if !b.Created.Before(a.End) {
			late++
			f.Violations = append(f.Violations, fmt.Sprintf("tenant B's Bundle %s was created %s after tenant A's load ended: not measured under contention",
				b.Name, b.Created.Sub(a.End)))
			continue
		}
		if b.Last.IsZero() {
			f.Violations = append(f.Violations, fmt.Sprintf("tenant B's Bundle %s did not end Verified under tenant A's load", b.Name))
		} else {
			f.Worst = max(f.Worst, b.Latency())
		}
		f.SlowestStep = max(f.SlowestStep, b.SlowestStep)
		if f.Backlog && b.SlowestStep > bound {
			f.Violations = append(f.Violations, fmt.Sprintf("tenant B's Bundle %s had a step of %s under tenant A's load, longer than %.2f times A's median step (%s) plus %s (%s): B's steps waited behind A's backlog",
				b.Name, b.SlowestStep, bc.Ratio, a.StepMedian, FairnessSlack, bound))
		}
		if a.End.Sub(b.Created) <= window {
			continue
		}
		f.Judged++
		if b.First.IsZero() || !b.First.Before(a.End) {
			f.Violations = append(f.Violations, fmt.Sprintf("tenant B's Bundle %s waited for all of tenant A's load: no environment was Verified until %s after A's load ended, though A's load ran %s after it was created, longer than B's %s alone",
				b.Name, firstAfter(b, a.End), a.End.Sub(b.Created), f.Alone))
		}
	}
	if f.Backlog {
		f.Checks = append(f.Checks, fmt.Sprintf("B's steps against A's backlog: judged: B's slowest step %s, at most %.2f times A's median step (%s) plus %s (%s)",
			f.SlowestStep, bc.Ratio, a.StepMedian, FairnessSlack, bound))
	}
	f.Checks = append(f.Checks, fmt.Sprintf("starvation: judged on %d of %d B Bundles (A's load outlasted them by more than %s)",
		f.Judged, len(loaded)-late, window))
	if f.Judged == 0 && late == 0 {
		f.Violations = append(f.Violations, fmt.Sprintf("contention not measured: tenant A's load ended within %s (B's slowest Bundle alone plus %s) of each of tenant B's Bundles' creation",
			window, FairnessSlack))
	}
	return f
}

// firstAfter is how long after aEnd b's first environment was Verified, or
// "never".
func firstAfter(b TenantBundle, aEnd time.Time) string {
	if b.First.IsZero() {
		return "never"
	}
	return b.First.Sub(aEnd).String()
}

// Timing times the Bundles created from their objects: each Bundle's
// creation and its steps' Verified conditions, retired steps included.
func (f *Fleet) Timing(ctx context.Context, created []Created) ([]TenantBundle, error) {
	steps, bundles, err := f.allSteps(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]*v1alpha1.Bundle{}
	for i := range bundles {
		byName[bundles[i].Name] = &bundles[i]
	}
	out := make([]TenantBundle, 0, len(created))
	for _, c := range created {
		b, ok := byName[c.Name]
		if !ok {
			return nil, fmt.Errorf("bundle %s is gone", c.Name)
		}
		tb := TenantBundle{Name: c.Name, Created: b.CreationTimestamp.Time}
		var last time.Time
		for i := range steps {
			if steps[i].Spec.BundleName != c.Name {
				continue
			}
			at, ok := lifecycle.VerifiedTime(&steps[i])
			if !ok {
				continue
			}
			tb.SlowestStep = max(tb.SlowestStep, at.Sub(steps[i].CreationTimestamp.Time))
			if tb.First.IsZero() || at.Before(tb.First) {
				tb.First = at
			}
			if at.After(last) {
				last = at
			}
		}
		if b.Status.Phase == "Verified" {
			tb.Last = last
		}
		out = append(out, tb)
	}
	return out, nil
}

// Load times the fleet's load from its steps, retired steps included: when
// its last step was Verified, once every Bundle settled Verified, and the
// median of its steps' times from creation to Verified.
func (f *Fleet) Load(ctx context.Context) (TenantLoad, error) {
	steps, _, err := f.allSteps(ctx)
	if err != nil {
		return TenantLoad{}, err
	}
	var l TenantLoad
	var times []time.Duration
	for i := range steps {
		at, ok := lifecycle.VerifiedTime(&steps[i])
		if !ok {
			continue
		}
		if at.After(l.End) {
			l.End = at
		}
		times = append(times, at.Sub(steps[i].CreationTimestamp.Time))
	}
	if len(times) > 0 {
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		l.StepMedian = times[(len(times)+1)/2-1]
	}
	return l, nil
}

// allSteps is the fleet's PromotionSteps and its Bundles. Retirement
// deletes a finished Bundle's Graph and steps and keeps the steps in the
// Bundle's status.retiredSteps (#1492); they are rebuilt as the invariants
// rebuild them (lifecycle.StepFromRetired).
func (f *Fleet) allSteps(ctx context.Context) ([]v1alpha1.PromotionStep, []v1alpha1.Bundle, error) {
	var list v1alpha1.PromotionStepList
	if err := f.E.Client.List(ctx, &list, client.InNamespace(f.NS)); err != nil {
		return nil, nil, fmt.Errorf("list steps: %w", err)
	}
	bundles, err := f.Bundles(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list Bundles: %w", err)
	}
	steps := list.Items
	live := map[string]bool{}
	for i := range steps {
		live[steps[i].Name] = true
	}
	for i := range bundles {
		for _, r := range bundles[i].Status.RetiredSteps {
			if !live[r.Name] {
				steps = append(steps, lifecycle.StepFromRetired(&bundles[i], r))
			}
		}
	}
	return steps, bundles, nil
}
