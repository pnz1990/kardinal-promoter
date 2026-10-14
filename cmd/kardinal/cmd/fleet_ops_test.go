// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// lcFleetPipeline is test → uat → prod, prod a fleet of n targets t00...
func lcFleetPipeline(n int) *v1alpha1.Pipeline {
	p := lcPipeline()
	var targets []v1alpha1.FleetTarget
	for i := range n {
		targets = append(targets, v1alpha1.FleetTarget{Name: fmt.Sprintf("t%02d", i)})
	}
	p.Spec.Environments[2].Fleet = &v1alpha1.FleetSpec{MaxConcurrent: 2, Targets: targets}
	return p
}

// TestFleetPauseCmd (#1835): kardinal pause --env <fleet> lists the fleet in
// spec.pausedFleets and says how to resume; resume --env removes it; a
// target or a plain environment is refused, naming what to pause instead.
//
// Covers FLEET-14.
func TestFleetPauseCmd(t *testing.T) {
	c := lcClient(t, lcFleetPipeline(3))
	var buf bytes.Buffer
	require.NoError(t, fleetPauseFn(&buf, c, "default", "app", "prod", true))
	assert.Equal(t, "Fleet prod of pipeline app paused. No further target starts; the targets in flight finish.\n"+
		"Resume with: kardinal resume app --env prod\n", buf.String())
	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "app"}, &p))
	assert.Equal(t, []string{"prod"}, p.Spec.PausedFleets)
	assert.False(t, p.Spec.Paused)

	err := fleetPauseFn(&buf, c, "default", "app", "prod-t01", true)
	require.ErrorIs(t, err, lifecycle.ErrInvalid)
	assert.ErrorContains(t, err, "pause app --env prod-t01: prod-t01 is a target of fleet prod")
	err = fleetPauseFn(&buf, c, "default", "app", "uat", true)
	assert.ErrorContains(t, err, "pause the whole pipeline with kardinal pause app")

	buf.Reset()
	require.NoError(t, fleetPauseFn(&buf, c, "default", "app", "prod", false))
	assert.Contains(t, buf.String(), "Fleet prod of pipeline app resumed.")
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "app"}, &p))
	assert.Empty(t, p.Spec.PausedFleets)
}

// TestRollbackCmd_Fleet (#1835): kardinal rollback --env <fleet> of a
// rollout stopped part way says the version, how many targets go back (each
// from what, at most six named), how many run it already, that maxConcurrent
// paces it and, for a paused fleet, that it waits for the resume.
//
// Covers FLEET-15.
func TestRollbackCmd_Fleet(t *testing.T) {
	p := lcFleetPipeline(10)
	p.Spec.PausedFleets = []string{"prod"}
	objs := []client.Object{p, lcBundle("app-v1", "app", "1", 0), lcBundle("app-v2", "app", "2", 100)}
	for i := range 10 {
		env := fmt.Sprintf("prod-t%02d", i)
		objs = append(objs, lcStep("app-v1", env, "Verified", 1+i))
		if i < 8 {
			objs = append(objs, lcStep("app-v2", env, "Verified", 101+i))
		}
	}
	c := lcClient(t, objs...)
	var buf bytes.Buffer
	require.NoError(t, rollbackFn(&buf, c, "default", "app", "prod", ""))
	created := newBundles(t, c, "app-v1", "app-v2")
	require.Len(t, created, 1)
	assert.Equal(t, "prod", created[0].Spec.Intent.TargetEnvironment)
	out := buf.String()
	assert.Contains(t, out, "Rolling back fleet prod of app to app-v1 (ghcr.io/org/app:1)")
	assert.Contains(t, out, "8 of 10 targets go back: prod-t00 from app-v2, prod-t01 from app-v2, prod-t02 from app-v2, "+
		"prod-t03 from app-v2, prod-t04 from app-v2, prod-t05 from app-v2, and 2 more\n")
	assert.Contains(t, out, "2 run app-v1 already and are Verified at once.")
	assert.Contains(t, out, "Fleet prod is paused: the rollback starts no target until kardinal resume app --env prod")
	assert.Contains(t, out, "The fleet's maxConcurrent paces the rollback.")
}
