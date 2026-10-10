// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// TestFleet_UnresolvedEnvironmentFailsClosed (D1): a step whose environment
// the Pipeline does not resolve to never runs with an empty spec (auto, the
// default path and health). It stays in its state, says why, and looks
// again: a fleet target whose selector has not resolved, and a step of an
// environment that became a fleet while it was in flight (the nodes shape
// Bundle's prod step after prod is edited into a fleet). Once the fleet
// resolves, the target's step starts.
//
// Covers FLEET-07.
func TestFleet_UnresolvedEnvironmentFailsClosed(t *testing.T) {
	selectorFleet := func() *v1alpha1.Pipeline {
		p := makePipeline("web")
		p.Spec.Environments[1].Fleet = &v1alpha1.FleetSpec{Selector: &v1alpha1.FleetSelector{
			MatchLabels: map[string]string{"tier": "prod"}}}
		return p
	}
	for _, tc := range []struct {
		name, env, state, want string
		noFleet                bool
	}{
		{name: "target of a fleet that is gone", env: "prod-eu", state: "Promoting", noFleet: true,
			want: "it is a target of fleet prod, which does not list it any more"},
		{name: "target of an unresolved fleet", env: "prod-eu", state: "Pending",
			want: `its selector has not been resolved yet`},
		{name: "environment that became a fleet", env: "prod", state: "Promoting",
			want: "it is now a fleet environment, whose targets are promoted as prod-<target>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step := makeStep("step", "web", "bundle-1", tc.env)
			step.Status.State = tc.state
			p := selectorFleet()
			if tc.noFleet {
				// The Pipeline lost its last fleet; the step still carries the label.
				p = makePipeline("web")
				step.Labels = map[string]string{"kardinal.io/fleet": "prod"}
			}
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithStatusSubresource(
				&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.Pipeline{},
			).WithObjects(step, p, makeBundle("bundle-1", "web")).Build()
			git := &countingGit{}
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: git,
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step", Namespace: "default"}}
			for range 2 { // idempotent
				res, err := r.Reconcile(context.Background(), req)
				require.NoError(t, err)
				assert.Equal(t, 30*time.Second, res.RequeueAfter)
			}
			var got v1alpha1.PromotionStep
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			assert.Equal(t, tc.state, got.Status.State, "the step does not move")
			assert.Contains(t, got.Status.Message, "cannot be resolved")
			assert.Contains(t, got.Status.Message, tc.want)
			assert.Zero(t, int(git.clones.Load()+git.pushes.Load()), "nothing is cloned or pushed")
		})
	}

	// Resolved: the target's step starts.
	p := selectorFleet()
	p.Status.Fleets = []v1alpha1.FleetStatus{{Environment: "prod", Targets: []v1alpha1.FleetTarget{{Name: "eu"}}}}
	step := makeStep("step", "web", "bundle-1", "prod-eu")
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.Pipeline{},
	).WithObjects(step, p, makeBundle("bundle-1", "web")).Build()
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "step", Namespace: "default"}})
	require.NoError(t, err)
	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "step", Namespace: "default"}, &got))
	assert.Equal(t, "Promoting", got.Status.State)
}

// TestFleet_PendingWaitsForAdmission (#1565 QA): a fleet target's step that
// the Graph has created but not read back yet (spec.admitted false) does no
// work: no git call, it stays Pending, says why and looks again. If pacing
// drops it in that window, kro deletes a step that pushed nothing, opened
// no PR and holds no finalizer duty (Pending holds no PR). Once the Graph
// sets admitted true, it starts.
//
// Covers FLEET-08.
func TestFleet_PendingWaitsForAdmission(t *testing.T) {
	p := makePipeline("web")
	p.Spec.Environments[1].Fleet = &v1alpha1.FleetSpec{Targets: []v1alpha1.FleetTarget{{Name: "eu"}}}
	step := labelled(makeStep("step", "web", "bundle-1", "prod-eu"))
	step.Labels["kardinal.io/fleet"] = "prod"
	no := false
	step.Spec.Admitted = &no
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithStatusSubresource(
		&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.Pipeline{},
	).WithObjects(step, p, makeBundle("bundle-1", "web")).Build()
	git := &countingGit{}
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: git,
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step", Namespace: "default"}}
	for range 3 { // idempotent; the first reconcile records nothing else either
		res, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, res.RequeueAfter)
	}
	var got v1alpha1.PromotionStep
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.Equal(t, "", got.Status.State, "still Pending")
	assert.Contains(t, got.Status.Message, "confirm this step's admission")
	assert.Zero(t, int(git.clones.Load()+git.pushes.Load()), "nothing is cloned or pushed")
	assert.Empty(t, got.Finalizers, "no PR finalizer duty")

	yes := true
	got.Spec.Admitted = &yes
	require.NoError(t, c.Update(context.Background(), &got))
	for range 3 {
		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
	}
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	assert.NotEqual(t, "", got.Status.State, "admitted: it starts")
	assert.NotContains(t, got.Status.Message, "admission")
}
