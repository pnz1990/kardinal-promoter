//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// The chaos tests run one at a time (no t.Parallel): each injects a fault
// into the controller, kro, the git server or the API server while a
// sustained load runs over ChaosPipelines Pipelines (a chain of four, the
// last behind a PR a reviewer merges), then stops the fault and checks that
// everything settles and every invariant holds.

// chaosLoad is the shared load: the Pipelines, a reviewer, and Bundles at
// the profile's SustainedRate (at least one every two seconds) for ChaosFor.
// fault runs on the test goroutine once the load started; it starts the
// chaos and returns the function that stops it.
func chaosLoad(t *testing.T, r *scale.Run, fault func() (stop func())) {
	t.Helper()
	envs := scale.Chain(4)
	envs[3].Approval = "pr-review"
	names := r.Fleet.Pipelines(t, "chaos", r.P.ChaosPipelines, envs)
	var repos []gitserver.Repo
	for _, tg := range r.Fleet.Targets() {
		repos = append(repos, tg.Repo)
	}
	rev := scale.Review(t, r.E.Git, nil, repos...)
	rate := r.P.SustainedRate
	if rate < 0.5 {
		rate = 0.5
	}
	load := make(chan scale.LoadStats)
	go func() { load <- r.Fleet.Sustained(context.Background(), t, names, rate, r.P.ChaosFor) }()
	stop := fault()
	r.Note("load", <-load)
	stop()
	r.E.WaitControllerLeads(t)
	r.Fleet.WaitSettled(t, r.P.Settle)
	rev.Stop()
	r.Note("prsMerged", rev.Merged())
}

// chaosAllow are the error logs a fault is expected to cause: the controller
// cannot reach the API server or the git server for a while.
var chaosAllow = []string{
	`context deadline exceeded`, `connection refused`, `connection reset by peer`, `i/o timeout`,
	`TLS handshake timeout`, `EOF`, `http2: client connection lost`, `the server is currently unable to handle the request`,
	`Too many requests`, `rate: Wait`, `client rate limiter`,
}

// TestScale_ChaosLeaderKill kills the controller leader with no grace
// period every 20 to 60 seconds while the load runs: the other replica must
// take over each time and every promotion must finish once.
// Covers SCALE-CHAOS-LEADER-01.
func TestScale_ChaosLeaderKill(t *testing.T) {
	r := scale.Begin(t)
	r.E.WaitControllerLeads(t)
	var kills *scale.Chaos
	chaosLoad(t, r, func() func() {
		kills = scale.Start(t, "leader kill", r.Rand.Between(20*time.Second, 60*time.Second), func(ctx context.Context) error {
			return scale.KillLeader(ctx, r.E)
		})
		return kills.Stop
	})
	r.Note("leaderKills", kills.Count())
	// A Pod deleted with no grace period runs on for a moment after the API
	// object is gone, and its bound ServiceAccount token is already
	// invalid: its last requests get 401, and its contexts are cancelled.
	r.Finish(scale.Allow(append(chaosAllow, `Unauthorized`, `context canceled`)...),
		func(o *invariants.Options) { o.MaxReconcileErrorRatio = 0.2 })
	if kills.Count() == 0 {
		t.Errorf("no leader was killed: %v", kills.Errors())
	}
}

// TestScale_ChaosKroRestart kills the kro controller every 45 to 75
// seconds while the load runs: Graphs must pick up where they were.
// Covers SCALE-CHAOS-KRO-01.
func TestScale_ChaosKroRestart(t *testing.T) {
	r := scale.Begin(t)
	var kills *scale.Chaos
	chaosLoad(t, r, func() func() {
		kills = scale.Start(t, "kro restart", r.Rand.Between(45*time.Second, 75*time.Second), func(ctx context.Context) error {
			return scale.RestartKro(ctx, r.E)
		})
		return kills.Stop
	})
	r.Note("kroRestarts", kills.Count())
	r.Finish(scale.Allow(chaosAllow...), func(o *invariants.Options) { o.MaxReconcileErrorRatio = 0.2 })
}

// TestScale_ChaosGitLatency adds 1.5 s of latency (0.5 s of jitter) to
// every git server response while the load runs, as a git host under load
// or across an ocean does: promotions slow down but none may fail. The test
// runner (the reviewer) reaches the git server directly.
// Covers SCALE-CHAOS-GITLATENCY-01.
func TestScale_ChaosGitLatency(t *testing.T) {
	r := scale.Begin(t)
	tp := scale.NewToxiproxy(t, r.E)
	ctx := context.Background()
	chaosLoad(t, r, func() func() {
		if err := tp.Latency(ctx, 1500, 500); err != nil {
			t.Fatal(err)
		}
		return func() { _ = tp.Reset(ctx) }
	})
	r.Finish(scale.Allow(chaosAllow...), func(o *invariants.Options) { o.MaxReconcileErrorRatio = 0.2 })
	assertNewestVerified(t, r)
}

// gitOutageBug is the SCM circuit breaker's: a 60-second outage opens it
// for ten minutes and fails every pr-review promotion in flight.
const gitOutageBug = 1476

// TestScale_ChaosGitOutage cuts the git server off for 60 seconds, twice,
// while the load runs, as a git host failover does: promotions must wait
// and retry, and the newest Bundle of every Pipeline must still end
// Verified. Covers SCALE-CHAOS-GITOUTAGE-01.
func TestScale_ChaosGitOutage(t *testing.T) {
	scale.KnownBug(t, gitOutageBug, "a 60s git outage keeps the SCM circuit open for 10 minutes; every pr-review promotion in flight fails")
	r := scale.Begin(t)
	tp := scale.NewToxiproxy(t, r.E)
	// The breaker's state is in memory: restart the controller afterwards,
	// so a circuit left open does not fail the next tests.
	t.Cleanup(func() { scale.RestartController(t, r.E) })
	ctx := context.Background()
	outages := 0
	chaosLoad(t, r, func() func() {
		for i := 0; i < 2; i++ {
			time.Sleep(r.P.ChaosFor / 4)
			if err := tp.SetEnabled(ctx, false); err != nil {
				t.Fatalf("git outage: %v", err)
			}
			outages++
			time.Sleep(time.Minute)
			if err := tp.SetEnabled(ctx, true); err != nil {
				t.Fatalf("end git outage: %v", err)
			}
		}
		return func() { _ = tp.Reset(ctx) }
	})
	r.Note("gitOutages", outages)
	r.Finish(scale.Allow(chaosAllow...), func(o *invariants.Options) { o.MaxReconcileErrorRatio = 0.3 })
	assertNewestVerified(t, r)
}

// TestScale_ChaosAPIThrottle puts the controller in an API Priority and
// Fairness level of one seat while the load runs, so the API server queues
// and refuses its requests (HTTP 429) as a busy shared control plane does.
// Covers SCALE-CHAOS-APF-01.
func TestScale_ChaosAPIThrottle(t *testing.T) {
	r := scale.Begin(t)
	var dispatched, rejected float64
	chaosLoad(t, r, func() func() {
		undo := scale.Throttle(t, r.E)
		return func() {
			var err error
			if dispatched, rejected, err = scale.ThrottleStats(context.Background(), r.E); err != nil {
				t.Errorf("throttle: %v", err)
			}
			undo()
		}
	})
	r.Note("throttledRequestsDispatched", dispatched)
	r.Note("throttledRequestsRejected", rejected)
	if dispatched == 0 {
		t.Errorf("no controller request went through the throttled priority level")
	}
	r.Finish(scale.Allow(chaosAllow...), func(o *invariants.Options) { o.MaxReconcileErrorRatio = 0.3 })
}

// TestScale_ChaosTokenRotation rotates the SCM token mid-flight, as a
// security team does: a new git server user's token replaces the old one in
// the controller's Secret and the Pipelines' Secret while Bundles promote.
// PRs opened after the rotation must be the new user's, and every promotion
// must finish. Covers SCALE-CHAOS-TOKEN-01.
func TestScale_ChaosTokenRotation(t *testing.T) {
	r := scale.Begin(t)
	user := "rotated-" + r.Fleet.NS[len(r.Fleet.NS)-8:]
	token := r.E.GitUser(t, user, []string{"write:repository", "write:issue"})
	users := r.E.GitUsers(t)
	var rotatedAt time.Time
	chaosLoad(t, r, func() func() {
		for _, tg := range r.Fleet.Targets() {
			if err := users.AddCollaborator(context.Background(), tg.Repo, user); err != nil {
				t.Fatalf("add %s to %s: %v", user, tg.Repo.Name, err)
			}
		}
		time.Sleep(r.P.ChaosFor / 3)
		rotatedAt = time.Now()
		r.E.SetSecretValue(t, framework.ControllerNamespace, framework.GitSecretName, "token", []byte(token))
		r.E.SetSecretValue(t, r.Fleet.NS, framework.GitSecretName, "token", []byte(token))
		t.Logf("SCM token rotated to user %s", user)
		return func() {}
	})
	r.Note("rotatedAt", rotatedAt.UTC().Format(time.RFC3339))
	rep := r.Finish()
	if rotatedAt.IsZero() || !rep.Pass {
		return
	}
	// Some PR opened well after the rotation is the new user's.
	authors := map[string]int{}
	for _, tg := range r.Fleet.Targets() {
		prs, err := r.E.Git.PullRequests(context.Background(), tg.Repo)
		if err != nil {
			t.Fatal(err)
		}
		for _, pr := range prs {
			authors[pr.Author]++
		}
	}
	r.Note("prAuthors", fmt.Sprint(authors))
	if authors[user] == 0 {
		var all []string
		for a, n := range authors {
			all = append(all, fmt.Sprintf("%s=%d", a, n))
		}
		t.Errorf("no PR is the rotated user %s's after the rotation: authors %s", user, strings.Join(all, ", "))
	}
}
