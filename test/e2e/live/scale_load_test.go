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

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// The load tests run one at a time (no t.Parallel), before the topology and
// race tests, so each owns the controller while it runs and its metrics
// window is its own.

// TestScale_LoadPipelines creates the profile's Pipelines (200 in full),
// each over a repo of its own, and one Bundle on each at once.
// Covers SCALE-LOAD-PIPELINES-01.
func TestScale_LoadPipelines(t *testing.T) {
	r := scale.Begin(t)
	start := time.Now()
	names := r.Fleet.Pipelines(t, "load", r.P.Pipelines, scale.Chain(r.P.PipelineEnvs))
	r.Note("pipelines", len(names))
	r.Note("pipelineSetupSeconds", int(time.Since(start).Seconds()))
	r.Note("burst", r.Fleet.Burst(t, names, len(names), 50))
	r.Finish(scale.AllVerified)
}

// TestScale_LoadBurst creates the profile's BurstBundles Bundles (1,000 in
// full) at once over BurstPipelines Pipelines, as CI does after an outage:
// on every Pipeline the newest Bundle must end Verified and the others
// Superseded. Covers SCALE-LOAD-BURST-01.
func TestScale_LoadBurst(t *testing.T) {
	r := scale.Begin(t)
	names := r.Fleet.Pipelines(t, "burst", r.P.BurstPipelines, scale.Chain(r.P.PipelineEnvs))
	r.Note("pipelines", len(names))
	r.Note("burst", r.Fleet.Burst(t, names, r.P.BurstBundles, 50))
	r.Finish()
	assertNewestVerified(t, r)
}

// assertNewestVerified checks that the newest Bundle of each Pipeline (by
// kardinal.io/created-at, the supersession order) ended Verified.
func assertNewestVerified(t *testing.T, r *scale.Run) {
	t.Helper()
	bundles, err := r.Fleet.Bundles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	newest := map[string]*v1alpha1.Bundle{}
	for i := range bundles {
		b := &bundles[i]
		if n := newest[b.Spec.Pipeline]; n == nil || lifecycle.CompareCreation(b, n) > 0 {
			newest[b.Spec.Pipeline] = b
		}
	}
	for p, b := range newest {
		if b.Status.Phase != "Verified" {
			t.Errorf("Pipeline %s: its newest Bundle %s is %q, want Verified", p, b.Name, b.Status.Phase)
		}
	}
}

// TestScale_LoadSustained creates Bundles at the profile's SustainedRate
// for SustainedFor over SustainedPipelines Pipelines (KARDINAL_E2E_SCALE_
// SUSTAINED_RATE and _FOR set them; the soak profile runs 5/s for 30
// minutes), then lets them settle. Covers SCALE-LOAD-SUSTAINED-01.
func TestScale_LoadSustained(t *testing.T) {
	r := scale.Begin(t)
	names := r.Fleet.Pipelines(t, "steady", r.P.SustainedPipelines, scale.Chain(r.P.PipelineEnvs))
	r.Note("pipelines", len(names))
	s := r.Fleet.Sustained(context.Background(), t, names, r.P.SustainedRate, r.P.SustainedFor)
	r.Note("sustained", s)
	// A load long enough to fill every Pipeline's history must find the
	// steady state, or the memory checks would fall back to the cold start.
	if perPipeline := r.P.SustainedRate * r.P.SustainedFor.Seconds() / float64(len(names)); perPipeline > 1.5*scale.HistoryLimit && s.WarmAt.IsZero() {
		t.Errorf("%.0f Bundles per Pipeline (over 1.5x historyLimit %d) but no warm baseline: some Pipeline never passed historyLimit",
			perPipeline, scale.HistoryLimit)
	}
	// The memory checks measure from the steady state: once every Pipeline
	// keeps HistoryLimit Bundles (the soak profile; zero otherwise).
	r.Finish(func(o *invariants.Options) { o.WarmAt = s.WarmAt })
	assertNewestVerified(t, r)
}

// TestScale_LatencySLO holds the controller to the profile's latency
// objective (test/e2e/README.md#latency-slo): SLOPipelines Pipelines of
// PipelineEnvs automatic environments each get one Bundle at once, as a
// monorepo release does, and the automatic steps' and the Bundles' latency
// quantiles must stay under the objective. Covers SCALE-LOAD-SLO-01.
func TestScale_LatencySLO(t *testing.T) {
	r := scale.Begin(t)
	names := r.Fleet.Pipelines(t, "slo", r.P.SLOPipelines, scale.Chain(r.P.PipelineEnvs))
	r.Note("pipelines", len(names))
	r.Note("burst", r.Fleet.Burst(t, names, len(names), 50))
	slo := r.P.SLO
	r.Finish(scale.AllVerified, func(o *invariants.Options) { o.SLO = &slo })
}

// TestScale_TwoTenants is two teams on one controller (#1577, #1578).
// Tenant A promotes one Bundle through a canary and a wave of TenantWave
// environments that all write one repository's branch. While A's wave is
// pushing, tenant B, in another namespace, promotes a two-environment
// Pipeline on its own repository. Each of B's steps must get its first
// reconcile (the PromotionStarted record) within TenantStartWithin of its
// creation. A's pushes must land at about one per environment: the
// metrics-push-efficiency invariant holds the refused pushes to at most half
// the landed ones. Covers SCALE-LOAD-TENANTS-01.
func TestScale_TwoTenants(t *testing.T) {
	r := scale.Begin(t)
	ctx := context.Background()
	a := r.Fleet.Pipeline(t, "tenant-a", scale.Waves(1, r.P.TenantWave))
	tenantB := scale.NewFleet(t, r.E, r.Rand)
	b := tenantB.Pipeline(t, "tenant-b", scale.Chain(2))
	r.Note("tenantWave", r.P.TenantWave)

	r.Fleet.MustCreateBundle(t, a.Name, scale.Tag(a.Name, 1))
	// B starts once A's wave is under way: a third of its steps exist.
	deadline := time.Now().Add(r.P.Settle)
	for {
		var steps v1alpha1.PromotionStepList
		if err := r.E.Client.List(ctx, &steps, client.InNamespace(r.Fleet.NS)); err != nil {
			t.Fatal(err)
		}
		if len(steps.Items) >= r.P.TenantWave/3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tenant A's wave never started: %d steps", len(steps.Items))
		}
		time.Sleep(time.Second)
	}
	bb := tenantB.MustCreateBundle(t, b.Name, scale.Tag(b.Name, 1))
	if !tenantB.WaitSettled(t, r.P.Settle) {
		t.Fatalf("tenant B's Bundle did not settle")
	}

	steps, err := r.E.Steps(ctx, tenantB.NS, b.Name, bb.Name)
	if err != nil {
		t.Fatal(err)
	}
	var worst time.Duration
	for i := range steps {
		s := &steps[i]
		var started v1alpha1.AuditEvent
		if err := r.E.Client.Get(ctx, client.ObjectKey{Namespace: tenantB.NS, Name: s.Name + "-started"}, &started); err != nil {
			t.Errorf("tenant B step %s: no PromotionStarted record: %v", s.Name, err)
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, started.Annotations[lifecycle.AnnotationCreatedAt])
		if err != nil {
			at = started.Spec.Timestamp.Time
		}
		wait := at.Sub(s.CreationTimestamp.Time)
		worst = max(worst, wait)
		if s.Status.State != "Verified" {
			t.Errorf("tenant B step %s ended %s: %s", s.Name, s.Status.State, s.Status.Message)
		}
	}
	r.Note("tenantBFirstReconcileSeconds", worst.Round(100*time.Millisecond).Seconds())
	if worst > r.P.TenantStartWithin {
		t.Errorf("a step of tenant B waited %s for its first reconcile while tenant A's wave pushed (limit %s)",
			worst.Round(100*time.Millisecond), r.P.TenantStartWithin)
	}
	r.Finish()
	// Tenant B's own invariants, in a report of their own (Dir follows the
	// test name); the controller's metrics and logs are in A's.
	t.Run("tenant-b", func(t *testing.T) {
		invariants.Check(t, r.E, invariants.Options{Namespace: tenantB.NS, Targets: tenantB.Targets(),
			Image: scale.ImageRepo, SeedTag: scale.SeedTag, Start: r.Start, Extra: map[string]interface{}{"tenant": "b"}})
	})
}

// TestScale_TenantFairness is #1577's acceptance: one namespace's large
// promotion must not starve another's. Tenant B, in another namespace, first
// promotes one Bundle on each of TenantBBundles three-environment Pipelines,
// 2 s apart, alone: its time alone on this controller and host. Then tenant A
// promotes a canary and a TenantWave-wide wave on one branch (its pushes take
// turns, #1586), and once a tenth of TenantWave steps exist B promotes a
// second Bundle on each Pipeline, 2 s apart. Fairness is judged against B's
// time alone and A's own steps (scale.JudgeFairness, #2252): none of B's
// Bundles under A's load waits for all of it (no environment Verified until
// A's load ended); when A's load is a backlog (A's median step more than
// scale.BacklogFactor times B's slowest step alone), no step of B's takes
// longer than A's median step, as a fair queue serves B ahead of A's
// backlog; and contention was measured. A host where A's load builds no
// backlog (32 cores) judges only starvation. B's step p99 (creation to
// Verified, the latency-slo invariant) stays within TenantStepP99 and its
// Bundles' p99 within TenantBundleP99, the second line of defence: on a
// GitHub runner with controller-runtime's default queue instead of the fair
// one, TenantStepP99 failed too (#2257). A's wave time is noted
// (tenantAWaveSeconds) to compare runs.
//
// Covers SCALE-LOAD-FAIR-01.
func TestScale_TenantFairness(t *testing.T) {
	r := scale.Begin(t)
	a := r.Fleet.Pipeline(t, "fair-a", scale.Waves(1, r.P.TenantWave))
	tenantFairness(t, r, func() {
		r.Fleet.MustCreateBundle(t, a.Name, scale.Tag(a.Name, 1))
	}, scale.BacklogCheck{Why: "A's wave pushes one branch, so its steps wait for the branch's turn at low priority (#1586), " +
		"not in the queue level B's steps are served from: there is no backlog for the fair queue to serve B ahead of"}, r.P.TenantStepP99)
}

// TestScale_TenantFairnessManyRepos is TestScale_TenantFairness with tenant
// A's load spread over TenantRepos five-environment Pipelines, each on its
// own repository, all promoted at once: no branch turns order them, so all
// of A's steps clone and push at normal priority, and only the work queue's
// fairness between namespaces lets B's steps through (#1577).
//
// Covers SCALE-LOAD-FAIR-01.
func TestScale_TenantFairnessManyRepos(t *testing.T) {
	r := scale.Begin(t)
	names := r.Fleet.Pipelines(t, "fairm", r.P.TenantRepos, scale.Chain(5))
	r.Note("tenantRepos", r.P.TenantRepos)
	stepP99 := r.P.TenantStepP99
	if r.P.TenantReposStepP99 > 0 {
		stepP99 = r.P.TenantReposStepP99
	}
	tenantFairness(t, r, func() {
		r.Fleet.Burst(t, names, len(names), 50)
	}, scale.BacklogCheck{Ratio: r.P.TenantBacklogRatio, Why: fmt.Sprintf("profile %s sets no TenantBacklogRatio: at %d repositories A's backlog is "+
		"too shallow to tell the fair queue from controller-runtime's default one (#2257); TenantStepP99 judges this profile, "+
		"and the nightly profile judges the backlog", r.P.Name, r.P.TenantRepos)}, stepP99)
}

// tenantFairness times tenant B's Bundles alone, then starts tenant A's
// load with startA, waits until a tenth of TenantWave steps exist, times
// B's Bundles again under it, and judges them (scale.JudgeFairness).
func tenantFairness(t *testing.T, r *scale.Run, startA func(), bc scale.BacklogCheck, stepP99 time.Duration) {
	t.Helper()
	ctx := context.Background()
	tenantB := scale.NewFleet(t, r.E, r.Rand)
	bNames := tenantB.Pipelines(t, "fair-b", r.P.TenantBBundles, scale.Chain(3))
	r.Note("tenantWave", r.P.TenantWave)

	// B alone, on the same controller and host: the baseline.
	aloneB := promoteEach(t, tenantB, bNames, 1)
	if !tenantB.WaitSettled(t, r.P.Settle) {
		t.Fatalf("tenant B's Bundles alone did not settle")
	}

	aStart := time.Now()
	startA()
	deadline := time.Now().Add(r.P.Settle)
	for {
		var steps v1alpha1.PromotionStepList
		if err := r.E.Client.List(ctx, &steps, client.InNamespace(r.Fleet.NS)); err != nil {
			t.Fatal(err)
		}
		if len(steps.Items) >= r.P.TenantWave/10 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tenant A's load never started: %d steps", len(steps.Items))
		}
		time.Sleep(time.Second)
	}
	// One Bundle per B Pipeline, 2 s apart, so they meet A's load at
	// different points.
	loadedB := promoteEach(t, tenantB, bNames, 2)
	if !tenantB.WaitSettled(t, r.P.Settle) {
		t.Fatalf("tenant B's Bundles did not settle")
	}
	if !r.Fleet.WaitSettled(t, r.P.Settle) {
		t.Fatalf("tenant A's Bundles did not settle")
	}
	r.Note("tenantAWaveSeconds", int(time.Since(aStart).Seconds()))

	load, err := r.Fleet.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	alone, err := tenantB.Timing(ctx, aloneB)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := tenantB.Timing(ctx, loadedB)
	if err != nil {
		t.Fatal(err)
	}
	fair := scale.JudgeFairness(alone, loaded, load, bc)
	for _, c := range fair.Checks {
		t.Log("fairness check " + c)
	}
	r.Note("fairnessChecks", strings.Join(fair.Checks, "; "))
	r.Note("tenantAStepMedianSeconds", load.StepMedian.Seconds())
	r.Note("tenantABacklog", fair.Backlog)
	r.Note("tenantBAloneSeconds", fair.Alone.Seconds())
	r.Note("tenantBSlowestStepAloneSeconds", fair.AloneStep.Seconds())
	r.Note("tenantBUnderASeconds", fair.Worst.Seconds())
	r.Note("tenantBSlowestStepUnderASeconds", fair.SlowestStep.Seconds())
	r.Note("tenantBJudgedForStarvation", fair.Judged)
	for _, b := range append(alone, loaded...) {
		t.Logf("tenant B's Bundle %s: created %s before A's load ended; first environment Verified after %s, the last after %s; slowest step %s",
			b.Name, load.End.Sub(b.Created), since(b.Created, b.First), since(b.Created, b.Last), b.SlowestStep)
	}
	for _, v := range fair.Violations {
		t.Error(v)
	}
	r.Finish(scale.AllVerified)
	// B's latency, with B's own targets.
	t.Run("tenant-b", func(t *testing.T) {
		slo := invariants.SLO{StepP99: stepP99, BundleP99: r.P.TenantBundleP99}
		invariants.Check(t, r.E, invariants.Options{Namespace: tenantB.NS, Outcome: invariants.OutcomeAllVerified, Targets: tenantB.Targets(),
			Image: scale.ImageRepo, SeedTag: scale.SeedTag, Start: r.Start, SLO: &slo,
			Extra: map[string]interface{}{"tenant": "b"}})
	})
}

// promoteEach creates the i-th Bundle of each of names, 2 s apart.
func promoteEach(t *testing.T, f *scale.Fleet, names []string, i int) []scale.Created {
	t.Helper()
	out := make([]scale.Created, 0, len(names))
	for n, name := range names {
		if n > 0 {
			time.Sleep(2 * time.Second)
		}
		out = append(out, f.MustCreateBundle(t, name, scale.Tag(name, i)))
	}
	return out
}

// since is at minus from, or "never" when at is zero.
func since(from, at time.Time) string {
	if at.IsZero() {
		return "never"
	}
	return at.Sub(from).String()
}
