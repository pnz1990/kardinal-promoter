//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"testing"
	"time"

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
	r.Finish(scale.AllVerified, func(o *invariants.Options) { o.SLO = &slo })
}
