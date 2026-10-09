// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// fleetPipeline is test → prod, a fleet of eu and us.
func fleetPipeline() *v1alpha1.Pipeline {
	p := pipeline("app", "test", "prod")
	p.Spec.Environments[1].Fleet = &v1alpha1.FleetSpec{Targets: []v1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}}}
	return p
}

// TestPlanRollback_Fleet (D1): kardinal rollback --env <fleet> rolls the
// whole fleet back: the target is chosen from the fleet's first target that
// has something deployed, and the rollback Bundle's targetEnvironment is the
// fleet, so its Graph promotes every target. A target can still be rolled
// back alone.
//
// Covers FLEET-07.
func TestPlanRollback_Fleet(t *testing.T) {
	c := newClient(t, fleetPipeline(),
		bundle("app-v1", "app", "v1", 0), bundle("app-v2", "app", "v2", 10),
		step("app-v1", "app", "test", "Verified", 1), step("app-v1", "app", "prod-us", "Verified", 2),
		step("app-v2", "app", "test", "Verified", 11), step("app-v2", "app", "prod-us", "Verified", 12),
	)
	plan, err := lifecycle.PlanRollback(context.Background(), c, lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
	require.NoError(t, err, "prod-eu ran nothing yet: the plan comes from prod-us")
	assert.Equal(t, "app-v1", plan.Target.Name)
	assert.Equal(t, "prod", plan.Bundle.Spec.Intent.TargetEnvironment, "the whole fleet")
	assert.Equal(t, "v1", plan.Bundle.Spec.Images[0].Tag)

	one, err := lifecycle.PlanRollback(context.Background(), c, lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod-us", Now: t0.Add(time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, "prod-us", one.Bundle.Spec.Intent.TargetEnvironment, "one target")

	_, err = lifecycle.PlanRollback(context.Background(), newClient(t, fleetPipeline()), lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
	assert.ErrorIs(t, err, lifecycle.ErrConflict)
	assert.ErrorContains(t, err, "nothing has been deployed to any target of fleet prod")
}

// TestHoldOf_Fleet (D1): a hold on a fleet (kardinal rollback --env <fleet>
// --hold) holds each target; a target's own hold wins; releasing a target
// held through its fleet says to release the fleet.
//
// Covers FLEET-07.
func TestHoldOf_Fleet(t *testing.T) {
	p := fleetPipeline()
	p.Spec.Holds = []v1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "app-rollback-a", Reason: "r",
		CreatedAt: &metav1.Time{Time: t0}}}
	h := lifecycle.HoldOf(p, "prod-eu")
	require.NotNil(t, h)
	assert.Equal(t, "app-rollback-a", h.Bundle)
	assert.Nil(t, lifecycle.HoldOf(p, "test"))
	assert.NotNil(t, lifecycle.HeldFrom(p, "prod-us", "app-v3"), "another Bundle is kept out of every target")
	assert.Nil(t, lifecycle.HeldFrom(p, "prod-us", "app-rollback-a"))

	p.Spec.Holds = append(p.Spec.Holds, v1alpha1.EnvironmentHold{Environment: "prod-eu", Bundle: "app-rollback-b", Reason: "r"})
	assert.Equal(t, "app-rollback-b", lifecycle.HoldOf(p, "prod-eu").Bundle, "a target's own hold wins")

	c := newClient(t, fleetPipeline())
	var cp v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(fleetPipeline()), &cp))
	cp.Spec.Holds = []v1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "app-rollback-a", Reason: "r"}}
	require.NoError(t, c.Update(context.Background(), &cp))
	_, err := lifecycle.ReleaseHold(context.Background(), c, ns, "app", "prod-us")
	assert.ErrorContains(t, err, "held through its fleet prod; release the fleet with --env prod")
	released, err := lifecycle.ReleaseHold(context.Background(), c, ns, "app", "prod")
	require.NoError(t, err)
	assert.Equal(t, "app-rollback-a", released.Bundle)
}
