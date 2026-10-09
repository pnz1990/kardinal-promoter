// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
)

// Run is one scale test: its profile, fleet, log collector and the numbers
// it reports.
type Run struct {
	T     *testing.T
	E     *framework.Env
	P     Profile
	Fleet *Fleet
	Logs  *invariants.Collector
	Start time.Time
	// Extra are the test's own numbers for the report.
	Extra map[string]interface{}

	shared bool
}

// Begin loads the profile, connects to the cluster, creates the test's
// fleet and starts collecting controller logs. A test that calls Begin runs
// alone (no t.Parallel): the controller's work queues and goroutines are its
// own, so the invariants check that they drain and stay flat.
func Begin(t *testing.T) *Run {
	t.Helper()
	return begin(t, false)
}

// BeginParallel is Begin for a test that runs in parallel with others
// (it calls t.Parallel): the work queues and goroutines are shared, so their
// end-of-run checks are reported, not enforced.
func BeginParallel(t *testing.T) *Run {
	t.Helper()
	t.Parallel()
	return begin(t, true)
}

func begin(t *testing.T, shared bool) *Run {
	t.Helper()
	p, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	e := framework.New(t)
	r := &Run{T: t, E: e, P: p, Start: time.Now(), Extra: map[string]interface{}{"profile": p.Name}, shared: shared}
	t.Logf("scale %s", p)
	r.Logs = invariants.Collect(t, e, invariants.Dir(t))
	r.Fleet = NewFleet(t, e)
	return r
}

// Note records a number for the report.
func (r *Run) Note(key string, v interface{}) {
	r.Extra[key] = v
	r.T.Logf("%s: %v", key, v)
}

// Finish waits for every Bundle to settle (the profile's Settle) and runs the
// invariants over the fleet; edit adjusts their options.
func (r *Run) Finish(edit ...func(*invariants.Options)) *invariants.Report {
	r.T.Helper()
	settleStart := time.Now()
	r.Fleet.WaitSettled(r.T, r.P.Settle)
	r.Note("settleSeconds", int(time.Since(settleStart).Seconds()))
	o := invariants.Options{
		Namespace: r.Fleet.NS, Targets: r.Fleet.Targets(), Image: ImageRepo, SeedTag: SeedTag,
		Start: r.Start, Logs: r.Logs, Metrics: true, Extra: r.Extra, SharedController: r.shared,
	}
	for _, fn := range edit {
		fn(&o)
	}
	return invariants.Check(r.T, r.E, o)
}

// Allow adds benign error-log patterns for the faults a test injects.
func Allow(patterns ...string) func(*invariants.Options) {
	return func(o *invariants.Options) {
		for _, p := range patterns {
			o.Allow = append(o.Allow, regexp.MustCompile(p))
		}
	}
}

// EnvKnownBugs set to 1 runs the parts of tests that reproduce open bugs
// (KnownBug) instead of skipping them.
const EnvKnownBugs = "KARDINAL_E2E_SCALE_KNOWN_BUGS"

// KnownBug marks the rest of the test as reproducing open bug issue (a
// pnz1990/kardinal-promoter issue number): it skips with a "KNOWN BUG #n"
// message, which test/e2e/report lists as a known bug instead of failing the
// suite, unless KARDINAL_E2E_SCALE_KNOWN_BUGS=1, when the test goes on and
// fails while the bug is open. Remove the call when the bug is fixed.
func KnownBug(t *testing.T, issue int, what string) {
	t.Helper()
	if v, _ := strconv.ParseBool(os.Getenv(EnvKnownBugs)); v {
		t.Logf("reproducing known bug #%d (%s=1): %s", issue, EnvKnownBugs, what)
		return
	}
	t.Skip(KnownBugMessage(issue, what))
}

// KnownBugMessage is the skip message test/e2e/report recognizes.
func KnownBugMessage(issue int, what string) string {
	return fmt.Sprintf("KNOWN BUG #%d https://github.com/pnz1990/kardinal-promoter/issues/%d: %s", issue, issue, what)
}

// Skip leaves out invariants the test's own assertions replace, with why.
func Skip(why string, checks ...string) func(*invariants.Options) {
	return func(o *invariants.Options) {
		if o.Skip == nil {
			o.Skip = map[string]string{}
		}
		for _, c := range checks {
			o.Skip[c] = why
		}
	}
}
