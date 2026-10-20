// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// rollbackOf marks b a rollback Bundle that restored of.
func rollbackOf(b *v1alpha1.Bundle, of string) *v1alpha1.Bundle {
	b.Labels = map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: b.Spec.Pipeline}
	b.Spec.Provenance.RollbackOf = of
	return stampAuto(b)
}

// stampAuto gives b the controller's automatic-rollback stamp and creator,
// as the controller writes them (lifecycle.IsAutoRollback).
func stampAuto(b *v1alpha1.Bundle) *v1alpha1.Bundle {
	if b.Annotations == nil {
		b.Annotations = map[string]string{}
	}
	b.Annotations[lifecycle.AnnotationCreatedBy] = lifecycle.ControllerCreator
	b.Spec.Actor = &v1alpha1.Actor{Kind: v1alpha1.ActorController, Principal: lifecycle.ControllerCreator, Via: v1alpha1.ViaAutoRollback}
	return b
}

// wideFleet is test → prod, a fleet of n targets t00..t<n-1>.
func wideFleet(n int) *v1alpha1.Pipeline {
	p := pipeline("app", "test", "prod")
	var targets []v1alpha1.FleetTarget
	for i := range n {
		targets = append(targets, v1alpha1.FleetTarget{Name: fmt.Sprintf("t%02d", i)})
	}
	p.Spec.Environments[1].Fleet = &v1alpha1.FleetSpec{MaxConcurrent: 10, Targets: targets}
	return p
}

// TestPlanRollback_FleetPartWay (#1835): the rollback of a fleet whose rollout
// stopped part way (10% of 50 targets failed: maxUnavailable) goes back to
// what the targets not reached yet run. The targets that run it stay, the
// failed and Verified ones go back, and one rollback Bundle targets the
// fleet, so maxConcurrent paces it.
//
// Covers FLEET-15.
func TestPlanRollback_FleetPartWay(t *testing.T) {
	objs := []client.Object{wideFleet(50), bundle("app-v1", "app", "v1", 0), phase(bundle("app-v2", "app", "v2", 100), "Failed")}
	for i := range 50 {
		env := fmt.Sprintf("prod-t%02d", i)
		objs = append(objs, step("app-v1", "app", env, "Verified", 1+i))
		switch {
		case i < 5:
			// Failed its health check: the change landed.
			failed := step("app-v2", "app", env, "Failed", 101+i)
			expiry := metav1.NewTime(t0.Add(2 * time.Hour))
			failed.Status.HealthCheckExpiry = &expiry
			objs = append(objs, failed)
		case i < 15:
			objs = append(objs, step("app-v2", "app", env, "Verified", 101+i))
		}
	}
	plan, err := lifecycle.PlanRollback(context.Background(), newClient(t, objs...), lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, "app-v1", plan.Target.Name)
	assert.Equal(t, "prod", plan.Bundle.Spec.Intent.TargetEnvironment)
	assert.Equal(t, "v1", plan.Bundle.Spec.Images[0].Tag)
	require.Len(t, plan.Fleet, 50)
	changes := 0
	for i, f := range plan.Fleet {
		assert.Equal(t, fmt.Sprintf("prod-t%02d", i), f.Environment, "fleet order")
		if f.Changes {
			changes++
			assert.Equal(t, "app-v2", f.Runs)
		}
	}
	assert.Equal(t, 15, changes, "the 5 failed and 10 Verified targets go back; 35 run v1 already")
}

// TestPlanRollback_FleetMixedCases (#1835): which version a fleet whose
// targets run different Bundles goes back to.
//
// Covers FLEET-15.
func TestPlanRollback_FleetMixedCases(t *testing.T) {
	tests := []struct {
		name    string
		objs    []client.Object
		want    string
		changes map[string]bool
		errIs   error
		errHas  string
	}{{
		// eu was rolled back alone to v0 (a rollback Bundle created after
		// v2); us runs v2. The rollback counts as v0, its creation time does
		// not make it the newest: the fleet goes to v0, never forward to v2.
		name: "a target rolled back alone counts as what it restored",
		objs: []client.Object{fleetPipeline(),
			bundle("app-v0", "app", "v0", 0), bundle("app-v1", "app", "v1", 10), bundle("app-v2", "app", "v2", 20),
			rollbackOf(bundle("app-rb", "app", "v0", 30), "app-v0"),
			step("app-v0", "app", "prod-eu", "Verified", 1), step("app-v0", "app", "prod-us", "Verified", 2),
			step("app-v2", "app", "prod-eu", "Verified", 21), step("app-v2", "app", "prod-us", "Verified", 22),
			step("app-rb", "app", "prod-eu", "Verified", 31)},
		want:    "app-v0",
		changes: map[string]bool{"prod-eu": false, "prod-us": true},
	}, {
		// The oldest version was rejected since: the fleet does not go
		// there, and the message names what each target runs.
		name: "the oldest version is rejected",
		objs: []client.Object{fleetPipeline(),
			rejected(bundle("app-v1", "app", "v1", 10)), bundle("app-v2", "app", "v2", 20),
			step("app-v1", "app", "prod-eu", "Verified", 11), step("app-v1", "app", "prod-us", "Verified", 12),
			step("app-v2", "app", "prod-us", "Verified", 21)},
		errIs:  lifecycle.ErrConflict,
		errHas: "run different Bundles (prod-eu runs app-v1, prod-us runs app-v2); going back to app-v1, the oldest of them, failed",
	}, {
		// The Bundle a target runs is gone (deleted by hand).
		name: "a Bundle a target runs is gone",
		objs: []client.Object{fleetPipeline(), bundle("app-v2", "app", "v2", 20),
			step("app-v1", "app", "prod-eu", "Verified", 11), step("app-v2", "app", "prod-us", "Verified", 21)},
		errIs:  lifecycle.ErrConflict,
		errHas: "cannot be read",
	}, {
		// Every target runs one version, but one runs it through a
		// rollback: that is one version, so the fleet goes to the one before.
		name: "the same artifacts through a rollback are one version",
		objs: []client.Object{fleetPipeline(),
			bundle("app-v0", "app", "v0", 0), bundle("app-v1", "app", "v1", 10), rollbackOf(bundle("app-rb", "app", "v1", 30), "app-v1"),
			step("app-v0", "app", "prod-eu", "Verified", 1), step("app-v0", "app", "prod-us", "Verified", 2),
			step("app-v1", "app", "prod-eu", "Verified", 11), step("app-v1", "app", "prod-us", "Verified", 12),
			step("app-rb", "app", "prod-us", "Verified", 31)},
		want:    "app-v0",
		changes: map[string]bool{"prod-eu": true, "prod-us": true},
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := lifecycle.PlanRollback(context.Background(), newClient(t, tc.objs...), lifecycle.RollbackRequest{
				Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
			if tc.errIs != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.errIs)
				assert.ErrorContains(t, err, tc.errHas)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, plan.Target.Name)
			got := map[string]bool{}
			for _, f := range plan.Fleet {
				got[f.Environment] = f.Changes
			}
			assert.Equal(t, tc.changes, got)
		})
	}
}

// TestPlanRollback_FleetEveryTargetRunsIt: --to a version every target runs
// already is refused.
//
// Covers FLEET-15.
func TestPlanRollback_FleetEveryTargetRunsIt(t *testing.T) {
	c := newClient(t, fleetPipeline(), bundle("app-v1", "app", "v1", 10),
		step("app-v1", "app", "prod-eu", "Verified", 11), step("app-v1", "app", "prod-us", "Verified", 12))
	_, err := lifecycle.PlanRollback(context.Background(), c, lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", ToBundle: "app-v1", Now: t0.Add(time.Hour)})
	assert.ErrorIs(t, err, lifecycle.ErrConflict)
	assert.ErrorContains(t, err, "runs app-v1 already")
}

// TestSetFleetPaused (#1835): pausing a fleet adds it to spec.pausedFleets
// once, resuming removes it; a fleet target, an environment that is not a
// fleet and an unknown one are refused with what to do instead, and an
// unknown Pipeline is ErrNotFound. Two fleets paused at once both stay
// paused (the update retries a conflict instead of overwriting the list).
//
// Covers FLEET-14.
func TestSetFleetPaused(t *testing.T) {
	p := fleetPipeline()
	p.Spec.Environments = append(p.Spec.Environments, v1alpha1.EnvironmentSpec{Name: "edge",
		Fleet: &v1alpha1.FleetSpec{Targets: []v1alpha1.FleetTarget{{Name: "a"}}}})
	c := newClient(t, p)
	ctx := context.Background()
	get := func() []string {
		var got v1alpha1.Pipeline
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(p), &got))
		return got.Spec.PausedFleets
	}
	require.NoError(t, lifecycle.SetFleetPaused(ctx, c, ns, "app", "prod", true, nil))
	require.NoError(t, lifecycle.SetFleetPaused(ctx, c, ns, "app", "prod", true, nil), "idempotent")
	assert.Equal(t, []string{"prod"}, get())

	for _, tc := range []struct{ env, has string }{
		{env: "prod-eu", has: "is a target of fleet prod; a fleet is paused as a whole: --env prod"},
		{env: "test", has: "is not a fleet; pause the whole pipeline with kardinal pause app"},
		{env: "nope", has: `has no environment "nope"`},
	} {
		err := lifecycle.SetFleetPaused(ctx, c, ns, "app", tc.env, true, nil)
		assert.ErrorIs(t, err, lifecycle.ErrInvalid, tc.env)
		assert.ErrorContains(t, err, tc.has, tc.env)
	}
	assert.ErrorIs(t, lifecycle.SetFleetPaused(ctx, c, ns, "missing", "prod", true, nil), lifecycle.ErrNotFound)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, f := range []string{"edge", "prod"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = lifecycle.SetFleetPaused(ctx, c, ns, "app", f, true, nil)
		}()
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.ElementsMatch(t, []string{"prod", "edge"}, get())

	require.NoError(t, lifecycle.SetFleetPaused(ctx, c, ns, "app", "prod", false, nil))
	require.NoError(t, lifecycle.SetFleetPaused(ctx, c, ns, "app", "prod", false, nil), "idempotent")
	require.NoError(t, lifecycle.SetFleetPaused(ctx, c, ns, "app", "test", false, nil), "resuming what is not paused changes nothing")
	assert.Equal(t, []string{"edge"}, get())
	assert.True(t, lifecycle.FleetPaused(&v1alpha1.Pipeline{Spec: v1alpha1.PipelineSpec{PausedFleets: []string{"edge"}}}, "edge"))
	assert.False(t, lifecycle.FleetPaused(nil, "edge"))
}

// TestHoldCurrent_Fleet (#1835): kardinal hold --env <fleet> holds every
// target with one spec.holds entry naming the fleet when the targets run one
// Bundle (a target with nothing deployed is not counted); targets on
// different Bundles are refused, naming them and what to do instead.
//
// Covers FLEET-15.
func TestHoldCurrent_Fleet(t *testing.T) {
	ctx := context.Background()
	req := lifecycle.HoldCurrentRequest{Namespace: ns, Pipeline: "app", Environment: "prod", Reason: "audit", Actor: "alice", Now: t0}
	c := newClient(t, fleetPipeline(), bundle("app-v1", "app", "v1", 10),
		step("app-v1", "app", "prod-eu", "Verified", 11))
	h, existed, err := lifecycle.HoldCurrent(ctx, c, req)
	require.NoError(t, err)
	assert.False(t, existed)
	assert.Equal(t, "prod", h.Environment, "one entry for the fleet")
	assert.Equal(t, "app-v1", h.Bundle)
	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(fleetPipeline()), &p))
	assert.Equal(t, "app-v1", lifecycle.HoldOf(&p, "prod-us").Bundle, "every target is held, the one without a version too")
	_, existed, err = lifecycle.HoldCurrent(ctx, c, req)
	require.NoError(t, err)
	assert.True(t, existed, "the same hold again changes nothing")

	mixed := newClient(t, fleetPipeline(), bundle("app-v1", "app", "v1", 10), bundle("app-v2", "app", "v2", 20),
		step("app-v1", "app", "prod-eu", "Verified", 11), step("app-v1", "app", "prod-us", "Verified", 12),
		step("app-v2", "app", "prod-us", "Verified", 21))
	_, _, err = lifecycle.HoldCurrent(ctx, mixed, req)
	assert.ErrorIs(t, err, lifecycle.ErrConflict)
	assert.ErrorContains(t, err, "run different Bundles (prod-eu runs app-v1, prod-us runs app-v2)")
	assert.ErrorContains(t, err, "kardinal pause app --env prod")

	_, _, err = lifecycle.HoldCurrent(ctx, newClient(t, fleetPipeline()), req)
	assert.ErrorIs(t, err, lifecycle.ErrConflict)
	assert.ErrorContains(t, err, "nothing runs in prod")
}

// TestPlanRollback_FleetNeverForward (#1835, QA): a target rolled back
// alone to v0, whose v0 Bundle history cleanup deleted, cannot be ordered:
// counting the rollback by its own creation time would make the fleet go to
// v1 and roll that target forward. The rollback is refused, asking for --to.
// Also without the deletion, the fleet never goes to a version a rollback
// in a target went back from.
//
// Covers FLEET-15.
func TestPlanRollback_FleetNeverForward(t *testing.T) {
	p := wideFleet(3)
	gone := []client.Object{p, bundle("app-v1", "app", "v1", 10), bundle("app-v2", "app", "v2", 20),
		rollbackOf(bundle("app-rb", "app", "v0", 30), "app-v0"),
		step("app-v1", "app", "prod-t00", "Verified", 11), step("app-rb", "app", "prod-t00", "Verified", 31),
		step("app-v1", "app", "prod-t01", "Verified", 12),
		step("app-v1", "app", "prod-t02", "Verified", 13), step("app-v2", "app", "prod-t02", "Verified", 21)}
	_, err := lifecycle.PlanRollback(context.Background(), newClient(t, gone...), lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
	require.Error(t, err)
	assert.ErrorIs(t, err, lifecycle.ErrConflict)
	assert.ErrorContains(t, err, "prod-t00 runs app-rb, which restored app-v0, and app-v0 cannot be read")
	assert.ErrorContains(t, err, "--to")

	// The same fleet with v0 kept: the rollback counts as v0, so the fleet
	// goes to v0, where t00 already is, and not to v1.
	rb := rollbackOf(bundle("app-rb", "app", "v0", 30), "app-v0")
	rb.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod-t00"}
	rb.Annotations[lifecycle.AnnotationRollbackFrom] = "app-v1"
	kept := []client.Object{p, bundle("app-v0", "app", "v0", 0), bundle("app-v1", "app", "v1", 10), bundle("app-v2", "app", "v2", 20), rb,
		step("app-v0", "app", "prod-t00", "Verified", 1), step("app-v0", "app", "prod-t01", "Verified", 2), step("app-v0", "app", "prod-t02", "Verified", 3),
		step("app-v1", "app", "prod-t00", "Verified", 11), step("app-rb", "app", "prod-t00", "Verified", 31),
		step("app-v1", "app", "prod-t01", "Verified", 12),
		step("app-v1", "app", "prod-t02", "Verified", 13), step("app-v2", "app", "prod-t02", "Verified", 21)}
	plan, err := lifecycle.PlanRollback(context.Background(), newClient(t, kept...), lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, "app-v0", plan.Target.Name, "never v1, which t00 rolled back from")
	for _, f := range plan.Fleet {
		if f.Environment == "prod-t00" {
			assert.False(t, f.Changes, "t00 runs v0's artifacts already")
		}
	}

	// --to app-v1 names it: t00 was rolled back from v1, but an explicit
	// --to may go there, as for one target.
	to, err := lifecycle.PlanRollback(context.Background(), newClient(t, kept...), lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", ToBundle: "app-v1", Now: t0.Add(time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, "app-v1", to.Target.Name)
}

// TestPlanRollback_FleetPerTargetEligibility (#1835, QA): the version the
// fleet goes back to must have been Verified in every target the rollback
// changes, as for the rollback of one target: a target where it Failed, or
// that joined after it and never ran it, refuses the rollback, naming each.
// A target that runs nothing yet gets it as its first version.
//
// Covers FLEET-15.
func TestPlanRollback_FleetPerTargetEligibility(t *testing.T) {
	failedV1 := step("app-v1", "app", "prod-t01", "Failed", 12)
	exp := metav1.NewTime(t0.Add(13 * time.Minute))
	failedV1.Status.HealthCheckExpiry = &exp
	objs := []client.Object{wideFleet(5), bundle("app-v1", "app", "v1", 10), bundle("app-v2", "app", "v2", 20),
		step("app-v1", "app", "prod-t00", "Verified", 11),
		failedV1, step("app-v2", "app", "prod-t01", "Verified", 22),
		step("app-v1", "app", "prod-t02", "Verified", 13), step("app-v2", "app", "prod-t02", "Verified", 23),
		step("app-v2", "app", "prod-t03", "Verified", 24)} // t03 joined after v1; t04 runs nothing
	for _, to := range []string{"", "app-v1"} {
		_, err := lifecycle.PlanRollback(context.Background(), newClient(t, objs...), lifecycle.RollbackRequest{
			Namespace: ns, Pipeline: "app", Environment: "prod", ToBundle: to, Now: t0.Add(time.Hour)})
		require.Error(t, err, "to=%q", to)
		assert.ErrorIs(t, err, lifecycle.ErrConflict)
		assert.ErrorContains(t, err, "fleet prod cannot go back to app-v1, which is not known to work in "+
			"prod-t01 (it was not Verified there), prod-t03 (never ran it)")
		assert.NotContains(t, err.Error(), "prod-t04", "a target with nothing deployed gets it as its first version")
	}

	// Without t01 and t03 the rollback goes ahead, and t04 gets v1.
	ok := []client.Object{wideFleet(3), bundle("app-v1", "app", "v1", 10), bundle("app-v2", "app", "v2", 20),
		step("app-v1", "app", "prod-t00", "Verified", 11),
		step("app-v1", "app", "prod-t01", "Verified", 13), step("app-v2", "app", "prod-t01", "Verified", 23)}
	plan, err := lifecycle.PlanRollback(context.Background(), newClient(t, ok...), lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, []lifecycle.FleetTargetRollback{{Environment: "prod-t00", Runs: "app-v1"},
		{Environment: "prod-t01", Runs: "app-v2", Changes: true}, {Environment: "prod-t02", Changes: true}}, plan.Fleet)
}

// TestPlanRollback_FleetRolledBackFromInATarget: every target runs v2, and
// the version before is v1, but a rollback in t01 went back from v1 earlier:
// the fleet does not return there unless --to names it.
//
// Covers FLEET-15.
func TestPlanRollback_FleetRolledBackFromInATarget(t *testing.T) {
	rb := rollbackOf(bundle("app-rb", "app", "v0", 15), "app-v0")
	rb.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod-t01"}
	rb.Annotations[lifecycle.AnnotationRollbackFrom] = "app-v1"
	objs := []client.Object{wideFleet(2), bundle("app-v0", "app", "v0", 0), bundle("app-v1", "app", "v1", 10), bundle("app-v2", "app", "v2", 20), rb,
		step("app-v1", "app", "prod-t00", "Verified", 11), step("app-v2", "app", "prod-t00", "Verified", 21),
		step("app-v1", "app", "prod-t01", "Verified", 12), step("app-rb", "app", "prod-t01", "Verified", 16), step("app-v2", "app", "prod-t01", "Verified", 22)}
	_, err := lifecycle.PlanRollback(context.Background(), newClient(t, objs...), lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
	assert.ErrorIs(t, err, lifecycle.ErrConflict)
	assert.ErrorContains(t, err, "prod-t01 (a rollback there went back from it)")
	_, err = lifecycle.PlanRollback(context.Background(), newClient(t, objs...), lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", ToBundle: "app-v1", Now: t0.Add(time.Hour)})
	assert.NoError(t, err, "--to names it")
}

// forge strips the controller's stamp from b: a client's own Bundle that
// only carries the rollback markers.
func forge(b *v1alpha1.Bundle) *v1alpha1.Bundle {
	b.Spec.Actor = &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "mallory"}
	b.Annotations[lifecycle.AnnotationCreatedBy] = "mallory"
	return b
}

// QA-3 on #2187: a forged rollback-from on a fleet target's "rollback" does
// not mark the version it names as rolled back from there.
func TestPlanRollback_FleetForgedRolledBackFrom(t *testing.T) {
	rb := forge(rollbackOf(bundle("app-rb", "app", "v0", 15), "app-v0"))
	rb.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod-t01"}
	rb.Annotations[lifecycle.AnnotationRollbackFrom] = "app-v1"
	objs := []client.Object{wideFleet(2), bundle("app-v0", "app", "v0", 0), bundle("app-v1", "app", "v1", 10), bundle("app-v2", "app", "v2", 20), rb,
		step("app-v1", "app", "prod-t00", "Verified", 11), step("app-v2", "app", "prod-t00", "Verified", 21),
		step("app-v1", "app", "prod-t01", "Verified", 12), step("app-v2", "app", "prod-t01", "Verified", 22)}
	plan, err := lifecycle.PlanRollback(context.Background(), newClient(t, objs...), lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
	require.NoError(t, err, "app-v1 is not taken for rolled back from on mallory's say")
	assert.Equal(t, "app-v1", plan.Target.Name)
}

// QA-3 on #2187: a target that runs a client's own "rollback" whose
// artifacts are not those of the version it names counts as itself, not
// as that version: the fleet is not sent to the version the markers name.
func TestPlanRollback_FleetForgedRollbackChain(t *testing.T) {
	forged := forge(rollbackOf(bundle("app-rb", "app", "v9", 30), "app-v0"))
	objs := []client.Object{fleetPipeline(),
		bundle("app-v0", "app", "v0", 0), bundle("app-v1", "app", "v1", 10), bundle("app-v2", "app", "v2", 20), forged,
		step("app-v0", "app", "prod-eu", "Verified", 1), step("app-v0", "app", "prod-us", "Verified", 2),
		step("app-v2", "app", "prod-eu", "Verified", 21), step("app-v2", "app", "prod-us", "Verified", 22),
		step("app-rb", "app", "prod-eu", "Verified", 31)}
	plan, err := lifecycle.PlanRollback(context.Background(), newClient(t, objs...), lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
	if err == nil {
		assert.NotEqual(t, "app-v0", plan.Target.Name, "the markers alone do not make prod-eu run app-v0")
	}
	// The same Bundle restoring app-v0's artifacts is followed: it does run app-v0.
	same := forge(rollbackOf(bundle("app-rb", "app", "v0", 30), "app-v0"))
	objs[4] = same
	plan, err = lifecycle.PlanRollback(context.Background(), newClient(t, objs...), lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, "app-v0", plan.Target.Name)
}

// QA-3 on #2187: a person's rollback of one fleet target (a human stamp,
// to app-v1's artifacts, away from app-v2) that ran there marks app-v2
// rolled back from in that target, so the fleet is not sent back to app-v2.
// The same Bundle that never ran there marks nothing.
//
// Covers RB-AUTO-FORGED-06.
func TestPlanRollback_FleetPersonsRollbackCounts(t *testing.T) {
	objsWith := func(ran bool) []client.Object {
		rb := forge(rollbackOf(bundle("app-rb", "app", "v1", 30), "app-v1"))
		rb.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "prod-t01"}
		rb.Annotations[lifecycle.AnnotationRollbackFrom] = "app-v2"
		objs := []client.Object{wideFleet(2), bundle("app-v0", "app", "v0", 0), bundle("app-v1", "app", "v1", 10), bundle("app-v2", "app", "v2", 20), rb,
			bundle("app-v3", "app", "v3", 40),
			step("app-v1", "app", "prod-t00", "Verified", 11), step("app-v2", "app", "prod-t00", "Verified", 21),
			step("app-v1", "app", "prod-t01", "Verified", 12), step("app-v2", "app", "prod-t01", "Verified", 22),
			step("app-v3", "app", "prod-t00", "Verified", 41), step("app-v3", "app", "prod-t01", "Verified", 42)}
		if ran {
			objs = append(objs, step("app-rb", "app", "prod-t01", "Verified", 31))
		}
		return objs
	}
	_, err := lifecycle.PlanRollback(context.Background(), newClient(t, objsWith(true)...), lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
	require.ErrorIs(t, err, lifecycle.ErrConflict, "prod-t01 was rolled back from app-v2")
	assert.Contains(t, err.Error(), "prod-t01 (a rollback there went back from it)")
	plan, err := lifecycle.PlanRollback(context.Background(), newClient(t, objsWith(false)...), lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, "app-v2", plan.Target.Name, "a rollback that never ran marks nothing")
}
