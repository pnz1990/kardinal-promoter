// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

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
	// Rand is the test's seeded random source (EnvSeed).
	Rand *RNG
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
	seed, err := Seed()
	if err != nil {
		t.Fatalf("%s: %v", EnvSeed, err)
	}
	e := framework.New(t)
	rng := NewRNG(seed, t.Name())
	r := &Run{T: t, E: e, P: p, Start: time.Now(), Rand: rng,
		Extra: map[string]interface{}{"profile": p.Name, "seed": seed}, shared: shared}
	t.Logf("scale %s; seed %d (replay: %s=%d)", p, seed, EnvSeed, seed)
	checkRaceBuild(t, e)
	r.Logs = invariants.Collect(t, e, invariants.Dir(t))
	r.Fleet = NewFleet(t, e, rng)
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
		RaceBuild: os.Getenv(EnvRace) == "1",
	}
	for _, fn := range edit {
		fn(&o)
	}
	return invariants.Check(r.T, r.E, o)
}

// AllVerified expects every Bundle of the test to end Verified
// (invariants.OutcomeAllVerified): one Bundle per Pipeline, or Bundles
// promoted one after the other. The default expects the newest Bundle of
// each Pipeline Verified and the others Verified or Superseded.
func AllVerified(o *invariants.Options) { o.Outcome = invariants.OutcomeAllVerified }

// Allow adds benign error-log patterns for the faults a test injects.
func Allow(patterns ...string) func(*invariants.Options) {
	return func(o *invariants.Options) {
		for _, p := range patterns {
			o.Allow = append(o.Allow, regexp.MustCompile(p))
		}
	}
}

// KnownBug marks the test as reproducing open bug issue (a
// pnz1990/kardinal-promoter issue number): an expected failure. It logs a
// "KNOWN BUG #n" line and the test goes on. test/e2e/report lists a known-bug
// test that fails as a known bug (action xfail) instead of a failure, and
// test/e2e/proof fails it once the issue is closed. A known-bug test that
// passes is failed here with "KNOWN BUG #n FIXED": the bug is fixed, so
// remove the call (and mark its coverage rows covered).
func KnownBug(t *testing.T, issue int, what string) {
	t.Helper()
	t.Log(KnownBugMessage(issue, what))
	t.Cleanup(func() {
		if !t.Failed() {
			t.Errorf("KNOWN BUG #%d FIXED: the test passed; remove scale.KnownBug(t, %d, ...) and mark its coverage rows covered", issue, issue)
		}
	})
}

// KnownBugMessage is the line KnownBug logs, which test/e2e/report
// recognizes.
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

// EnvRace is set to 1 by hack/e2e/components/kardinal.sh when it built the
// controller with -race.
const EnvRace = "KARDINAL_E2E_RACE"

// raceVersion is the ControllerVersion of the -race build (kardinal.sh).
const raceVersion = "e2e-race"

// checkRaceBuild fails the test when the suite asked for a -race controller
// (EnvRace=1) and the controller runs another build: the controller writes
// its version to the kardinal-version ConfigMap.
func checkRaceBuild(t *testing.T, e *framework.Env) {
	t.Helper()
	if os.Getenv(EnvRace) != "1" {
		return
	}
	var cm corev1.ConfigMap
	key := types.NamespacedName{Namespace: framework.ControllerNamespace, Name: "kardinal-version"}
	if err := e.Client.Get(context.Background(), key, &cm); err != nil {
		t.Fatalf("%s=1 but the controller version is unknown: %v", EnvRace, err)
	}
	if v := cm.Data["version"]; v != raceVersion {
		t.Fatalf("%s=1 but the controller runs version %q, not the -race build %q", EnvRace, v, raceVersion)
	}
}
