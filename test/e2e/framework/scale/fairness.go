// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestScale_TenantFairness* time tenant B's Bundles twice in one run: alone,
// before tenant A's load starts, and under it. Fairness is judged against
// B's own time alone, on the same controller and host, not by whether B
// ends before A: on a fast host A's load ends before a fair B can (#2252).
//
// What tells a starved B is when its first environment is Verified, not how
// long it takes. With a queue that holds B until A drains (a test-only
// patch, on 32 CPUs and on a kind node capped at 4), B's first environment
// was Verified 1 to 3 s after A's load ended in every run; with the fair
// queue, well before. How much slower B is under A's load depends on the
// host as much as on the queue (the table on #2252): that bound
// (FairnessFactor) only catches a gross slowdown.

// FairnessFactor is how many times its slowest Bundle alone a B Bundle may
// take under A's load. It is sized for the smallest host the suite runs on,
// where A's load saturates the CPU and B's own work slows down however
// fairly the queue orders it: on a kind node capped at 4 CPUs (a GitHub
// runner's), with the fair queue, B's Bundles took 25 to 30 s under
// TestScale_TenantFairnessManyRepos' load against 4 to 5 s alone, up to 7.5
// times. A queue that holds B until A drains made them 57 to 61 s there,
// over 10 times. On a fast host A's load is too short to slow B that much
// whatever the queue does; the starvation check is what catches it there.
const FairnessFactor = 10

// FairnessSlack is added to the bound and the starvation window: the
// objects' timestamps are whole seconds, so a duration between two of them
// is up to 2 s off.
const FairnessSlack = 2 * time.Second

// TenantBundle is one of tenant B's Bundles, timed from its objects: its
// creation, its first environment Verified (zero if none was) and its last
// (zero unless the Bundle ended Verified).
type TenantBundle struct {
	Name                 string
	Created, First, Last time.Time
}

// Latency is the Bundle's time from its creation to its last environment
// Verified (the latency-slo invariant's Bundle end to end).
func (b TenantBundle) Latency() time.Duration { return b.Last.Sub(b.Created) }

// Fairness is the verdict on tenant B's Bundles under A's load.
type Fairness struct {
	// Alone is the slowest of B's Bundles alone; Bound is what a B Bundle
	// may take under A's load; Worst is the slowest that did.
	Alone, Bound, Worst time.Duration
	// Judged counts the B Bundles A's load outlasted long enough to tell
	// whether they waited for all of it.
	Judged     int
	Violations []string
}

// JudgeFairness judges tenant B's Bundles under tenant A's load (loaded)
// against B's Bundles alone (alone), A's load having ended at aEnd:
//   - starvation: a loaded Bundle that A's load outlasted by more than B's
//     slowest Bundle alone, plus FairnessSlack, has an environment Verified
//     before A's load ended (the timestamps are whole seconds: the same
//     second is not before). Its first environment is a third of the work
//     B did alone in that time; one with none Verified waited for all of
//     A's load, however short A's load was. A host A's load saturates,
//     where a fair B's first environment can take longer than that, has
//     A's load outlast B's Bundles by far more (45 s and more on 4 CPUs);
//   - each loaded Bundle ends within FairnessFactor times the slowest alone
//     Bundle, plus FairnessSlack;
//   - contention was measured: every loaded Bundle was created before A's
//     load ended, and at least one could be judged for starvation.
func JudgeFairness(alone, loaded []TenantBundle, aEnd time.Time) Fairness {
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
	}
	f.Bound = FairnessFactor*f.Alone + FairnessSlack
	window := f.Alone + FairnessSlack
	late := 0
	for _, b := range loaded {
		if !b.Created.Before(aEnd) {
			late++
			f.Violations = append(f.Violations, fmt.Sprintf("tenant B's Bundle %s was created %s after tenant A's load ended: not measured under contention",
				b.Name, b.Created.Sub(aEnd)))
			continue
		}
		if b.Last.IsZero() {
			f.Violations = append(f.Violations, fmt.Sprintf("tenant B's Bundle %s did not end Verified under tenant A's load", b.Name))
		} else {
			f.Worst = max(f.Worst, b.Latency())
			if b.Latency() > f.Bound {
				f.Violations = append(f.Violations, fmt.Sprintf("tenant B's Bundle %s took %s under tenant A's load, over %d times its %s alone plus %s (%s)",
					b.Name, b.Latency(), FairnessFactor, f.Alone, FairnessSlack, f.Bound))
			}
		}
		if aEnd.Sub(b.Created) <= window {
			continue
		}
		f.Judged++
		if b.First.IsZero() || !b.First.Before(aEnd) {
			f.Violations = append(f.Violations, fmt.Sprintf("tenant B's Bundle %s waited for all of tenant A's load: no environment was Verified until %s after A's load ended, though A's load ran %s after it was created, longer than B's %s alone",
				b.Name, firstAfter(b, aEnd), aEnd.Sub(b.Created), f.Alone))
		}
	}
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

// LastVerified is the latest Verified condition of the fleet's steps,
// retired steps included: when its load ended, once every Bundle settled
// Verified.
func (f *Fleet) LastVerified(ctx context.Context) (time.Time, error) {
	steps, _, err := f.allSteps(ctx)
	if err != nil {
		return time.Time{}, err
	}
	var last time.Time
	for i := range steps {
		if at, ok := lifecycle.VerifiedTime(&steps[i]); ok && at.After(last) {
			last = at
		}
	}
	return last, nil
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
