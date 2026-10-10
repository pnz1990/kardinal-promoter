//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
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
	r.Finish()
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
	r.Note("sustained", r.Fleet.Sustained(context.Background(), t, names, r.P.SustainedRate, r.P.SustainedFor))
	r.Finish()
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
	r.Finish(func(o *invariants.Options) { o.SLO = &slo })
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
// promotion must not starve another's. Tenant A promotes a canary and a
// TenantWave-wide wave on one branch. Once a third of A's steps exist,
// tenant B, in another namespace, promotes TenantBBundles Bundles of a
// three-environment Pipeline one after the other. B's step latency
// (creation to Verified, the latency-slo invariant) must stay at p99 within
// TenantStepP99 while A runs, and at least TenantBBundles-1 of B's Bundles
// must finish before A's wave does, or the test did not measure contention.
// A's wave time is noted (tenantAWaveSeconds) to compare runs.
//
// Covers SCALE-LOAD-FAIR-01.
func TestScale_TenantFairness(t *testing.T) {
	r := scale.Begin(t)
	ctx := context.Background()
	a := r.Fleet.Pipeline(t, "fair-a", scale.Waves(1, r.P.TenantWave))
	tenantB := scale.NewFleet(t, r.E, r.Rand)
	b := tenantB.Pipeline(t, "fair-b", scale.Chain(3))
	r.Note("tenantWave", r.P.TenantWave)

	aStart := time.Now()
	ab := r.Fleet.MustCreateBundle(t, a.Name, scale.Tag(a.Name, 1))
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
	aDone := func() bool {
		var bundle v1alpha1.Bundle
		err := r.E.Client.Get(ctx, client.ObjectKey{Namespace: r.Fleet.NS, Name: ab.Name}, &bundle)
		return err == nil && scale.TerminalPhase(bundle.Status.Phase)
	}
	during := 0
	for i := 1; i <= r.P.TenantBBundles; i++ {
		tenantB.MustCreateBundle(t, b.Name, scale.Tag(b.Name, i))
		if !tenantB.WaitSettled(t, r.P.Settle) {
			t.Fatalf("tenant B's Bundle %d did not settle", i)
		}
		if !aDone() {
			during++
		}
	}
	r.Note("tenantBBundlesDuringA", during)
	if during < r.P.TenantBBundles-1 {
		t.Errorf("only %d of tenant B's %d Bundles finished while A's wave ran: nothing was measured under contention",
			during, r.P.TenantBBundles)
	}
	if !r.Fleet.WaitSettled(t, r.P.Settle) {
		t.Fatalf("tenant A's Bundle did not settle")
	}
	r.Note("tenantAWaveSeconds", int(time.Since(aStart).Seconds()))
	r.Finish()
	// B's latency, with B's own targets.
	t.Run("tenant-b", func(t *testing.T) {
		slo := invariants.SLO{StepP99: r.P.TenantStepP99}
		invariants.Check(t, r.E, invariants.Options{Namespace: tenantB.NS, Targets: tenantB.Targets(),
			Image: scale.ImageRepo, SeedTag: scale.SeedTag, Start: r.Start, SLO: &slo,
			Extra: map[string]interface{}{"tenant": "b"}})
	})
}
