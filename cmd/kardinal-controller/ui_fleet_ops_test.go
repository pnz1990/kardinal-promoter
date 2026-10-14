// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// uiFleetOpsPipeline is test → uat → prod, prod a fleet of eu, us, ap.
func uiFleetOpsPipeline() *v1alpha1.Pipeline {
	p := uiLcPipeline()
	p.Spec.Environments[2].Fleet = &v1alpha1.FleetSpec{MaxConcurrent: 1,
		Targets: []v1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}, {Name: "ap"}}}
	return p
}

// TestUIAPI_FleetPauseResume (#1835): POST /pause and /resume with an
// environment pause and resume that fleet only (spec.pausedFleets), not the
// Pipeline; a fleet target or an environment that is not a fleet is a 400
// naming what to do instead; the pipelines list marks the fleet paused.
//
// Covers FLEET-14.
func TestUIAPI_FleetPauseResume(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(uiFleetOpsPipeline()).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	key := types.NamespacedName{Namespace: "default", Name: "app"}

	w := uiLcPost(t, c, "/api/v1/ui/pause", `{"pipeline":"app","environment":"prod"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "fleet prod of app paused: no further target starts")
	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, key, &p))
	assert.Equal(t, []string{"prod"}, p.Spec.PausedFleets)
	assert.False(t, p.Spec.Paused, "only the fleet")

	got := pipelineListResponse([]v1alpha1.Pipeline{p}, nil, nil, nil, uiLcT0, nil)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].EnvironmentTopology[2].Fleet)
	assert.True(t, got[0].EnvironmentTopology[2].Fleet.Paused)
	assert.False(t, got[0].Paused)

	for body, has := range map[string]string{
		`{"pipeline":"app","environment":"prod-eu"}`: "a target of fleet prod",
		`{"pipeline":"app","environment":"uat"}`:     "is not a fleet",
	} {
		w = uiLcPost(t, c, "/api/v1/ui/pause", body)
		assert.Equal(t, http.StatusBadRequest, w.Code, body)
		assert.Contains(t, w.Body.String(), has, body)
	}

	w = uiLcPost(t, c, "/api/v1/ui/resume", `{"pipeline":"app","environment":"prod"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, c.Get(ctx, key, &p))
	assert.Empty(t, p.Spec.PausedFleets)
}

// TestUIAPI_FleetRollback (#1835): POST /rollback with a fleet environment
// creates one rollback Bundle for the fleet, and the response lists each
// target with what it runs and whether it goes back; a paused fleet says
// that no target starts until it is resumed.
//
// Covers FLEET-15.
func TestUIAPI_FleetRollback(t *testing.T) {
	p := uiFleetOpsPipeline()
	p.Spec.PausedFleets = []string{"prod"}
	objs := []client.Object{p, uiLcBundle("app-v1", "1", 0), uiLcBundle("app-v2", "2", 10),
		uiLcStep("app-v1", "prod-eu", "Verified", 1), uiLcStep("app-v1", "prod-us", "Verified", 2),
		uiLcStep("app-v1", "prod-ap", "Verified", 3),
		uiLcStep("app-v2", "prod-eu", "Verified", 11), uiLcStep("app-v2", "prod-us", "Verified", 12)}
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(objs...).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	w := uiLcPost(t, c, "/api/v1/ui/rollback", `{"pipeline":"app","environment":"prod"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var resp uiRollbackResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "app-v1", resp.RollbackOf, "ap was not reached: the fleet goes back to what it runs")
	assert.Equal(t, []uiFleetTargetRollback{{Environment: "prod-eu", Runs: "app-v2", Changes: true},
		{Environment: "prod-us", Runs: "app-v2", Changes: true}, {Environment: "prod-ap", Runs: "app-v1"}}, resp.Fleet)
	assert.Contains(t, resp.Message, "rolling fleet prod back to app-v1: 2 of 3 targets go back")
	assert.Contains(t, resp.Message, "the fleet is paused")
	created := uiLcCreated(t, c, "app-v1", "app-v2")
	require.Len(t, created, 1)
	assert.Equal(t, "prod", created[0].Spec.Intent.TargetEnvironment)
}
