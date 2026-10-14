// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// fleetIncident is the fake cluster of TestJourney_FleetIncident: the
// Pipeline, the Bundles and the PromotionSteps the lifecycle functions read
// (the CLI's and the UI API's), and one compactSim per Bundle evaluating its
// Graph with kro's CEL.
type fleetIncident struct {
	t   *testing.T
	ctx context.Context
	c   client.Client
	at  time.Time
}

func (f *fleetIncident) pipeline() *kardinalv1alpha1.Pipeline {
	f.t.Helper()
	var p kardinalv1alpha1.Pipeline
	require.NoError(f.t, f.c.Get(f.ctx, client.ObjectKey{Namespace: "default", Name: "app"}, &p))
	return &p
}

// sim builds the Graph of bundle from the Pipeline as it is now (a Pipeline
// change rebuilds the Graph of a Bundle in flight in place), keeping steps.
func (f *fleetIncident) sim(bundle *kardinalv1alpha1.Bundle, steps map[string]string) *compactSim {
	f.t.Helper()
	p := f.pipeline()
	res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: bundle})
	require.NoError(f.t, err)
	assertKroValid(f.t, res.Graph)
	sim := newCompactSim(f.t, res.Graph)
	sim.fleet = map[string]string{}
	for _, e := range res.Environments {
		if fl := graph.FleetOf(p, e); fl != "" {
			sim.fleet[e] = fl
		}
	}
	if steps != nil {
		sim.steps = steps
	}
	return sim
}

// record writes sim's steps of bundle as PromotionSteps: a Failed step
// failed its health check, so its change landed.
func (f *fleetIncident) record(bundle string, sim *compactSim) {
	f.t.Helper()
	for env, state := range sim.steps {
		f.at = f.at.Add(time.Second)
		s := &kardinalv1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: bundle + "-" + env, Namespace: "default",
			Labels: map[string]string{lifecycle.LabelPipeline: "app", "kardinal.io/bundle": bundle, "kardinal.io/environment": env}},
			Spec: kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: bundle, Environment: env}}
		var cur kardinalv1alpha1.PromotionStep
		exists := f.c.Get(f.ctx, client.ObjectKeyFromObject(s), &cur) == nil
		if exists {
			s = &cur
		}
		s.Status.State = state
		switch state {
		case "Verified":
			meta := metav1.Condition{Type: "Verified", Status: metav1.ConditionTrue, Reason: "Verified", LastTransitionTime: metav1.NewTime(f.at)}
			s.Status.Conditions = []metav1.Condition{meta}
		case "Failed":
			exp := metav1.NewTime(f.at)
			s.Status.HealthCheckExpiry = &exp
		}
		if exists {
			require.NoError(f.t, f.c.Update(f.ctx, s))
		} else {
			s.CreationTimestamp = metav1.NewTime(f.at)
			require.NoError(f.t, f.c.Create(f.ctx, s))
		}
	}
}

func (f *fleetIncident) bundle(name, tag string) *kardinalv1alpha1.Bundle {
	f.t.Helper()
	f.at = f.at.Add(time.Minute)
	b := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
		CreationTimestamp: metav1.NewTime(f.at), Labels: map[string]string{lifecycle.LabelPipeline: "app"}},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app",
			Images:     []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: tag}},
			Provenance: &kardinalv1alpha1.BundleProvenance{CommitSHA: "sha-" + tag}}}
	require.NoError(f.t, f.c.Create(f.ctx, b))
	return b
}

// inFlightOf counts the fleet targets of sim with a step that is not settled.
func inFlightOf(sim *compactSim) int {
	n := 0
	for env, st := range sim.steps {
		if sim.fleet[env] != "" && st != "Verified" && st != "Superseded" {
			n++
		}
	}
	return n
}

// TestJourney_FleetIncident (#1835) walks an incident on a 20-target fleet
// with maxConcurrent 5 and maxUnavailable "10%" (2 targets) through the
// functions the CLI and the UI call, and each Graph as kro evaluates it:
//
//  1. v1 runs everywhere. v2 starts its first wave of 5.
//  2. The operator pauses the fleet mid-wave (kardinal pause --env prod): no
//     free place starts a target, the 5 in flight finish (1 Verified, 2
//     Failed, 2 still in flight).
//  3. kardinal rollback --env prod: one rollback version back to v1, which
//     the 15 targets v2 never reached run; 3 targets go back.
//  4. The rollback starts nothing while the fleet is paused; resumed, it
//     never has more than 5 targets in flight, and a target removed from
//     the Pipeline during the rollback leaves its wave.
//  5. kardinal hold --env prod holds the whole fleet on the rollback, and a
//     new version v3 starts no target.
//
// Covers FLEET-14, FLEET-15.
func TestJourney_FleetIncident(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	var targets []kardinalv1alpha1.FleetTarget
	for i := range 20 {
		targets = append(targets, kardinalv1alpha1.FleetTarget{Name: fmt.Sprintf("t%02d", i)})
	}
	pct := intstr.FromString("10%")
	p := pipelineOf("app", kardinalv1alpha1.EnvironmentSpec{Name: "test"},
		kardinalv1alpha1.EnvironmentSpec{Name: "prod", Fleet: &kardinalv1alpha1.FleetSpec{MaxConcurrent: 5, MaxUnavailable: &pct, Targets: targets}})
	f := &fleetIncident{t: t, ctx: context.Background(), at: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC),
		c: fake.NewClientBuilder().WithScheme(s).WithObjects(p).
			WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()}

	// 1. v1 everywhere.
	v1 := f.bundle("app-v1", "v1")
	sim := f.sim(v1, nil)
	sim.steps["test"] = "Verified"
	for range 10 {
		sim.advance()
		for env, st := range sim.steps {
			if st == "" {
				sim.steps[env] = "Verified"
			}
		}
	}
	_, complete := sim.wave()
	require.True(t, complete, "v1 is Verified in every target")
	f.record(v1.Name, sim)

	// v2: the first wave of 5.
	v2 := f.bundle("app-v2", "v2")
	sim = f.sim(v2, nil)
	sim.steps["test"] = "Verified"
	assert.Len(t, sim.advance(), 6, "test and 5 targets")

	// 2. Pause mid-wave.
	require.NoError(t, lifecycle.SetFleetPaused(f.ctx, f.c, "default", "app", "prod", true))
	sim = f.sim(v2, sim.steps)
	sim.steps["prod-t00"] = "Verified"
	sim.steps["prod-t01"] = "Failed"
	sim.steps["prod-t02"] = "Failed"
	before := len(sim.steps)
	sim.advance()
	assert.Len(t, sim.steps, before, "paused: free places start nothing")
	wave, _ := sim.wave()
	assert.Contains(t, wave, "prod-t03", "a target in flight keeps its step")
	f.record(v2.Name, sim)

	// 3. Roll the fleet back.
	plan, err := lifecycle.PlanRollback(f.ctx, f.c, lifecycle.RollbackRequest{Namespace: "default", Pipeline: "app",
		Environment: "prod", Actor: "alice", Now: f.at})
	require.NoError(t, err)
	assert.Equal(t, "app-v1", plan.Target.Name)
	var back []string
	for _, tg := range plan.Fleet {
		if tg.Changes {
			back = append(back, tg.Environment)
		}
	}
	assert.Equal(t, []string{"prod-t00", "prod-t01", "prod-t02"}, back,
		"t00 Verified and t01, t02 failed on v2 go back; t03, t04 in flight never landed v2")
	rb := plan.Bundle
	rb.Name = "app-rollback-1"
	f.at = f.at.Add(time.Minute)
	rb.CreationTimestamp = metav1.NewTime(f.at)
	require.NoError(t, f.c.Create(f.ctx, rb))

	// 4. The rollback waits for the resume, then paces at 5.
	rsim := f.sim(rb, nil)
	rsim.steps["test"] = "Verified"
	assert.Equal(t, []string{"test"}, rsim.advance(), "paused: the rollback starts no target")
	require.NoError(t, lifecycle.SetFleetPaused(f.ctx, f.c, "default", "app", "prod", false))
	rsim = f.sim(rb, rsim.steps)
	removed := false
	for round := range 30 {
		rsim.advance()
		require.LessOrEqual(t, inFlightOf(rsim), 5, "round %d: maxConcurrent paces the rollback", round)
		if round == 1 && !removed {
			// A target removed from the Pipeline during the rollback.
			cur := f.pipeline()
			fl := cur.Spec.Environments[1].Fleet
			fl.Targets = append(fl.Targets[:10:10], fl.Targets[11:]...)
			require.NoError(t, f.c.Update(f.ctx, cur))
			removed = true
			steps := rsim.steps
			delete(steps, "prod-t10") // the Bundle reconciler retires it (FLEET-04)
			rsim = f.sim(rb, steps)
			continue
		}
		for env, st := range rsim.steps {
			if st == "" {
				rsim.steps[env] = "Verified"
			}
		}
	}
	wave, complete = rsim.wave()
	assert.True(t, complete, "the rollback is Verified in every target that remains")
	assert.NotContains(t, wave, "prod-t10", "the removed target left the rollback")
	assert.Len(t, wave, 20, "test and 19 targets")
	f.record(rb.Name, rsim)

	// 5. Hold the fleet on the rollback; v3 starts no target.
	h, _, err := lifecycle.HoldCurrent(f.ctx, f.c, lifecycle.HoldCurrentRequest{Namespace: "default", Pipeline: "app",
		Environment: "prod", Reason: "INC-1835", Actor: "alice", Now: f.at})
	require.NoError(t, err)
	assert.Equal(t, rb.Name, h.Bundle)
	assert.Equal(t, "prod", h.Environment)
	v3 := f.bundle("app-v3", "v3")
	vsim := f.sim(v3, nil)
	vsim.steps["test"] = "Verified"
	assert.Equal(t, []string{"test"}, vsim.advance(), "the fleet is held on the rollback")
}
