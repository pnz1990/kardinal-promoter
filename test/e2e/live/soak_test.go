//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/soak"
)

// The enterprise-day soak (#2403, test/e2e/README.md#soak-suite): one
// cluster, three teams and an org's shared gates, and the features of v0.13
// and v0.14 together under steady load for KARDINAL_E2E_SOAK_DURATION
// (default an hour), with an upgrade, a leader kill, freezes, an automatic
// rollback and a consent change on a timer while the load runs. Its
// invariants are checked every KARDINAL_E2E_SOAK_CHECK (default 3m) over
// what changed since the pass before; the first one that does not hold ends
// the soak, dumps the estate and fails the test naming it.

// Soak knobs.
const (
	// EnvSoakDuration is how long the load runs (a Go duration, default
	// soakDefault). The episodes run at their share of it, one after
	// another.
	envSoakDuration = "KARDINAL_E2E_SOAK_DURATION"
	// envSoakCheck is the time between the checker's passes.
	envSoakCheck = "KARDINAL_E2E_SOAK_CHECK"
	// envSoakRate is the CI load, Bundles a minute over the teams' Pipelines
	// (default 4; people, the agent, the evidence and fleet versions come
	// on top, about 2 more).
	envSoakRate = "KARDINAL_E2E_SOAK_RATE"

	soakDefault = 60 * time.Minute
	// soakHistory is every soak Pipeline's historyLimit: the warm-up
	// creates that many Bundles of each, so the kept Bundles stop growing
	// before the memory baseline.
	soakHistory = 10
)

// soakDurationOf parses a knob, or returns def when it is unset.
func soakDurationOf(t *testing.T, env string, def time.Duration) time.Duration {
	t.Helper()
	v := os.Getenv(env)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		t.Fatalf("%s=%q: want a positive Go duration such as 60m", env, v)
	}
	return d
}

// soakDay is the run: the estate, the load, the checker and what they
// recorded for the report.
type soakDay struct {
	t     *testing.T
	e     *framework.Env
	d     time.Duration
	rng   *scale.RNG
	seed  int64
	start time.Time
	logs  *invariants.Collector
	rel   *framework.Release
	reg   *framework.Registry
	check *soak.Checker
	// pub is the release's receipt key, cluster the receipts' cluster ID.
	pub, cluster string

	a, b, c *soakTenant
	// org consents to share its gates (kardinal.io/policy-shareable);
	// private does not, until the consent episode.
	org, private string
	// intruder is a namespace whose Pipeline points at team a's repo with
	// team c's token: it must never push there.
	intruder, intruderBundle string

	// load is the load generators' context; loadCancel stops them, and
	// botsCancel the bots that answer them (tickets, approvals, reviews)
	// once everything settled.
	load                   context.Context
	loadCancel, botsCancel context.CancelFunc
	loadWG, botsWG         sync.WaitGroup
	// asApprover is a client acting as team a's approver, asRogue as the
	// rogue agent's user; malloryKubeconfig is mallory's (the verifier
	// role, not an allowed verifier); rogue is the rogue AgentIdentity.
	asApprover, asRogue client.Client
	malloryKubeconfig   string
	rogue               string
	// final is set for the checker's last pass, over the whole day.
	final bool

	mu sync.Mutex
	// warmAt is when the warm-up ended: the memory baselines start there.
	warmAt time.Time
	// consentAt is when private consented (zero before).
	consentAt time.Time
	// windows are the episodes' times, reported apart in the reconcile
	// error ratio (an upgrade's rollout, a leader kill).
	windows []soakWindow
	// expect are checks an episode left for later passes: what was in
	// flight must have finished by its deadline.
	expect []soakExpectation
	// signed is each receipt's signature (status.bundle) and referrers as
	// first seen signed: neither may change (#2313).
	signed map[string]signedReceipt
	// heaps are the controller Pods' heap and RSS samples.
	heaps []heapSample
	// loadErrors are Bundle creates that failed twice.
	loadErrors []string
	// errorPasses counts the reconcile-error passes judged and unjudged
	// (Prometheus unreachable, or too few reconciles); promDown counts the
	// passes in a row Prometheus could not answer.
	errorJudged, errorUnjudged, promDown int
	// judged counts, per invariant, the steps a receipts or audit pass
	// judged (live and retired), for the report.
	judged map[string]int
	// tickets is each fake ticket's decision; rejected counts rejections.
	tickets  map[string]string
	rejected int
	// approved is the Bundles the approver approved at prod.
	approved map[string]bool
	// paused stops a load generator for an episode (the agent's, c-app's).
	paused map[string]bool
	// probes are Bundles an episode made to be refused something; the
	// receipts checks skip their expectations.
	probes map[string]bool
	// notes are the report's numbers.
	notes map[string]interface{}
}

// soakWindow is an episode's time span.
type soakWindow struct {
	Name     string    `json:"name"`
	From, To time.Time `json:"-"`
	FromS    string    `json:"from"`
	ToS      string    `json:"to"`
}

// soakExpectation is a check due at deadline.
type soakExpectation struct {
	name     string
	deadline time.Time
	check    func(ctx context.Context) error
}

// soakEpisode is one timed event of the day: it runs at its share of the
// day (at), or when the episode before it ended if that is later.
type soakEpisode struct {
	name string
	at   float64
	run  func(ctx context.Context) error
}

// TestSoak_EnterpriseDay runs the enterprise day (see the comment above the
// soak knobs and test/e2e/README.md#soak-suite):
//
//   - team a: agents ship (an auto AgentIdentity) and verify (a verify
//     gate on prod counts two runs of an allowed verifier agent), a
//     ServiceNow change request and a person's approval naming its ticket
//     gate prod, and an evidence Pipeline promotes the team's registry image
//     by digest;
//   - team b: PR review on prod merged by a reviewer bot, a Jira change
//     request, and the org's gates read through policyNamespaces with
//     consent;
//   - team c: a 31-environment fleet in the compact Graph shape, and a
//     podinfo release whose test environment rolls back by itself.
//
// Each team pushes with its own git token and its receipt referrers go only
// under its own registry prefix once the upgrade sets perNamespace:
// required. The day's episodes: the warm-up, an agents-only freeze, a
// forged and a suspended agent's verdict, a stop-the-line freeze, the
// upgrade, a team pushing another team's image, an automatic rollback, the
// leader killed with promotions, receipts and a change request in flight,
// and a consent change.
//
// Covers SOAK-DAY-01, SOAK-DAY-02, SOAK-DAY-03, SOAK-DAY-04, SOAK-DAY-05, SOAK-DAY-06.
func TestSoak_EnterpriseDay(t *testing.T) {
	s := newSoakDay(t)
	s.estate()
	s.t.Logf("soak: %s of load, checks every %s; seed %d (replay: %s=%d)", s.d, s.check.Interval, s.seed, scale.EnvSeed, s.seed)

	ctx := s.check.Start(context.Background())
	s.start = time.Now()
	s.startLoad(ctx)
	defer s.stopAll()
	s.day(ctx)
	if v := s.check.Violation(); v != nil {
		s.stopAll()
		t.Fatalf("the soak stopped at its first violation (diagnostics in %s): %v", invariants.Dir(t), v)
	}
	s.finish()
}

// newSoakDay connects to the cluster and starts the checker and the log
// collector.
func newSoakDay(t *testing.T) *soakDay {
	t.Helper()
	seed, err := scale.Seed()
	if err != nil {
		t.Fatalf("%s: %v", scale.EnvSeed, err)
	}
	e := framework.New(t)
	s := &soakDay{
		t: t, e: e, d: soakDurationOf(t, envSoakDuration, soakDefault), seed: seed, rng: scale.NewRNG(seed, t.Name()),
		reg:    framework.NewRegistry(t),
		signed: map[string]signedReceipt{}, tickets: map[string]string{}, approved: map[string]bool{},
		paused: map[string]bool{}, probes: map[string]bool{}, notes: map[string]interface{}{"seed": seed}, judged: map[string]int{},
	}
	s.notes["duration"] = s.d.String()
	s.rel = e.ExistingRelease(t, "kardinal-promoter", framework.ControllerNamespace)
	e.WaitControllerLeads(t)
	s.pub = receiptPublicKey(t, e)
	s.cluster = clusterIdentity(t, e)
	s.logs = invariants.Collect(t, e, invariants.Dir(t))
	t.Cleanup(s.logs.Stop)
	s.check = &soak.Checker{Interval: soakDurationOf(t, envSoakCheck, 3*time.Minute), Dump: s.dump}
	s.addChecks()
	return s
}

// at is the time of share f of the day.
func (s *soakDay) at(f float64) time.Time {
	return s.start.Add(time.Duration(f * float64(s.d)))
}

// span is share f of the day, at least floor.
func (s *soakDay) span(f float64, floor time.Duration) time.Duration {
	return max(floor, time.Duration(f*float64(s.d)))
}

// day runs the episodes in order, each at its time, and returns at the end
// of the day or at the first violation.
func (s *soakDay) day(ctx context.Context) {
	for _, ep := range s.episodes() {
		if !s.sleepUntil(ctx, s.at(ep.at)) {
			return
		}
		began := time.Now()
		s.t.Logf("soak T+%s: episode %s", began.Sub(s.start).Round(time.Second), ep.name)
		err := ep.run(ctx)
		s.note("episode."+ep.name+".seconds", int(time.Since(began).Seconds()))
		if err != nil {
			if ctx.Err() == nil {
				s.check.Fail("episode-"+ep.name, err)
			}
			return
		}
	}
	s.sleepUntil(ctx, s.at(1))
}

// episodes are the day's events (section 2 of #2403's design).
func (s *soakDay) episodes() []soakEpisode {
	return []soakEpisode{
		{"warm-up", 5.0 / 60, s.warmUp},
		{"agents-frozen", 15.0 / 60, s.agentsFrozen},
		{"forged-verdicts", 20.0 / 60, s.forgedVerdicts},
		{"stop-the-line", 25.0 / 60, s.stopTheLine},
		{"upgrade", 30.0 / 60, s.upgrade},
		{"cross-tenant-image", 35.0 / 60, s.crossTenantImage},
		{"auto-rollback", 40.0 / 60, s.autoRollback},
		{"leader-killed", 45.0 / 60, s.leaderKilled},
		{"consent", 50.0 / 60, s.consent},
	}
}

// sleepUntil waits for at; false when the soak ended first.
func (s *soakDay) sleepUntil(ctx context.Context, at time.Time) bool {
	d := time.Until(at)
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// note records a number for the report.
func (s *soakDay) note(key string, v interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notes[key] = v
}

// count adds n to a counter of the report.
func (s *soakDay) count(key string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _ := s.notes[key].(int)
	s.notes[key] = c + n
}

// window records an episode's span.
func (s *soakDay) window(name string, from, to time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.windows = append(s.windows, soakWindow{Name: name, From: from, To: to,
		FromS: from.UTC().Format(time.RFC3339), ToS: to.UTC().Format(time.RFC3339)})
}

// openWindow opens episode window name now, until closeWindow ends it: a
// pass during the episode already judges it as one.
func (s *soakDay) openWindow(name string) (from time.Time, closeWindow func()) {
	from = time.Now()
	s.mu.Lock()
	i := len(s.windows)
	s.windows = append(s.windows, soakWindow{Name: name, From: from, To: from.Add(24 * time.Hour), FromS: from.UTC().Format(time.RFC3339)})
	s.mu.Unlock()
	return from, func() {
		// The rollout's settling: two minutes after the episode.
		to := time.Now().Add(2 * time.Minute)
		s.mu.Lock()
		s.windows[i].To, s.windows[i].ToS = to, to.UTC().Format(time.RFC3339)
		s.mu.Unlock()
	}
}

// later leaves check for the first pass after deadline.
func (s *soakDay) later(name string, deadline time.Time, check func(ctx context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expect = append(s.expect, soakExpectation{name: name, deadline: deadline, check: check})
}

// isPaused reports whether an episode paused load generator name.
func (s *soakDay) isPaused(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused[name]
}

// pause stops (or, with false, restarts) load generator name.
func (s *soakDay) pause(name string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused[name] = on
}

// probe marks a Bundle an episode made to be refused.
func (s *soakDay) probe(bundle string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probes[bundle] = true
}

func (s *soakDay) isProbe(bundle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probes[bundle]
}

// soakWait polls cond every 2 seconds until it holds, the soak ends or
// timeout passes; the error names what it waited for and what it saw last.
func soakWait(ctx context.Context, timeout time.Duration, what string, cond func(ctx context.Context) (bool, string, error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	last := ""
	for {
		ok, seen, err := cond(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			last = err.Error()
		case err == nil:
			last = seen
			if ok {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: not within %s; last: %s", what, timeout, last)
		case <-time.After(2 * time.Second):
		}
	}
}

// stopAll stops every goroutine the soak started; it is safe to call twice.
func (s *soakDay) stopAll() {
	s.mu.Lock()
	cancel := s.loadCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.loadWG.Wait()
	if s.botsCancel != nil {
		s.botsCancel()
	}
	s.botsWG.Wait()
}
