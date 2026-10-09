// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestPlanRollback_ChartBundles: a chart Bundle (from a Helm Subscription)
// rolls back to the previous chart version Verified in the environment; an
// earlier Bundle with the same chart version, and Bundles of other types,
// are skipped. The rollback Bundle carries the chart.
func TestPlanRollback_ChartBundles(t *testing.T) {
	chartBundle := func(name, version string, minute int) *v1alpha1.Bundle {
		b := bundle(name, "app", "", minute)
		b.Spec.Type, b.Spec.Images = "chart", nil
		b.Spec.Chart = &v1alpha1.ChartRef{RepoURL: "https://charts.example.com", Name: "podinfo",
			Version: version, Digest: "sha256:" + version}
		return b
	}
	assert.True(t, lifecycle.HasArtifacts(chartBundle("c", "1.0.0", 0)), "a chart version is an artifact")
	assert.False(t, lifecycle.SameArtifacts(chartBundle("a", "1.0.0", 0), chartBundle("b", "1.1.0", 0)))
	assert.True(t, lifecycle.SameArtifacts(chartBundle("a", "1.0.0", 0), chartBundle("b", "1.0.0", 0)))

	c := newClient(t,
		pipeline("app", "test", "uat", "prod"),
		chartBundle("c1", "6.13.0", 0), bundle("img", "app", "9", 5),
		chartBundle("c2", "6.14.0", 10), chartBundle("c2-again", "6.14.0", 20), chartBundle("c3", "6.15.0", 30),
		step("c1", "app", "prod", "Verified", 1), step("img", "app", "prod", "Verified", 6),
		step("c2", "app", "prod", "Verified", 11), step("c2-again", "app", "prod", "Verified", 21),
		step("c3", "app", "prod", "Verified", 31),
	)
	plan, err := lifecycle.PlanRollback(context.Background(), c, lifecycle.RollbackRequest{
		Namespace: ns, Pipeline: "app", Environment: "prod", Actor: "alice", Now: t0.Add(time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, "c3", plan.CurrentName)
	assert.Equal(t, "c2-again", plan.Target.Name)
	require.NotNil(t, plan.Bundle.Spec.Chart)
	assert.Equal(t, "chart", plan.Bundle.Spec.Type)
	assert.Equal(t, "6.14.0", plan.Bundle.Spec.Chart.Version)
	assert.Empty(t, plan.Bundle.Spec.Images)

	// Rolling back to a Bundle with the deployed chart version is refused.
	_, err = lifecycle.PlanRollback(context.Background(), newClient(t,
		pipeline("app", "test", "uat", "prod"),
		chartBundle("c2", "6.14.0", 10), chartBundle("c2-again", "6.14.0", 20),
		step("c2", "app", "prod", "Verified", 11), step("c2-again", "app", "prod", "Verified", 21),
	), lifecycle.RollbackRequest{Namespace: ns, Pipeline: "app", Environment: "prod", Now: t0.Add(time.Hour)})
	require.ErrorIs(t, err, lifecycle.ErrConflict)
}
