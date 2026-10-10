// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// gitNeverCalled fails the test when the controller touches git: a layout:
// branch step renders in its RenderRun Job, never in the controller.
type gitNeverCalled struct{ mockGit }

// TestRenderStep_RunsInTheRenderRun (#1447, QA on #1515): a layout: branch
// step runs only the render step in the controller: it records
// status.renderRequestedAt (the Graph then creates the RenderRun) and waits
// in Promoting without cloning anything; once the Graph mirrors a Succeeded
// RenderRun onto spec.live.renders it takes the pushed commit and goes on to
// the health check.
func TestRenderStep_RunsInTheRenderRun(t *testing.T) {
	ctx := context.Background()
	p := makePipeline("web")
	p.Spec.Environments[0].Layout = "branch"
	step := makeStep("web-b1-test", "web", "b1", "test")
	b := makeBundle("b1", "web")
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		WithObjects(step, p, b).Build()
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &gitNeverCalled{mockGit{cloneErr: assert.AnError}},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	key := types.NamespacedName{Name: step.Name, Namespace: "default"}
	reconcile := func() *v1alpha1.PromotionStep {
		t.Helper()
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		require.NoError(t, err)
		var got v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, key, &got))
		return &got
	}

	got := reconcile() // Pending → Promoting, step list recorded
	require.Equal(t, "Promoting", got.Status.State)
	var names []string
	for _, s := range got.Status.Steps {
		names = append(names, s.Name)
	}
	assert.Equal(t, []string{"render", "health-check"}, names)
	got = reconcile()
	assert.Equal(t, "Promoting", got.Status.State)
	require.NotNil(t, got.Status.RenderRequestedAt, "the Graph creates the RenderRun once this is set")
	assert.Equal(t, "false", got.Status.Outputs["renderPullRequest"])
	got = reconcile()
	assert.Equal(t, "Promoting", got.Status.State, "waits for the RenderRun")
	assert.Contains(t, got.Status.Message, "waiting for the Graph to create the RenderRun")

	got.Spec.Live = &v1alpha1.PromotionStepLive{Renders: []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Running"}}}
	require.NoError(t, c.Update(ctx, got))
	got = reconcile()
	assert.Equal(t, "Promoting", got.Status.State)
	assert.Contains(t, got.Status.Message, "rendering in RenderRun rr: Running")

	got.Spec.Live.Renders = []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Succeeded", Result: &v1alpha1.RenderRunResult{
		CommitSHA: "0123456789abcdef0123456789abcdef01234567", Branch: "env/test", DryCommit: "d", Renderer: "kustomize", Objects: 2}}}
	require.NoError(t, c.Update(ctx, got))
	got = reconcile()
	assert.Equal(t, "HealthChecking", got.Status.State)
	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", got.Status.Outputs["commitSHA"], "the health check waits for the pushed render")
}

// TestRenderStep_FailedRenderFailsTheStep: a Failed RenderRun fails the step
// with its message.
func TestRenderStep_FailedRenderFailsTheStep(t *testing.T) {
	ctx := context.Background()
	p := makePipeline("web")
	p.Spec.Environments[0].Layout = "branch"
	step := makeStep("web-b1-test", "web", "b1", "test")
	step.Spec.Live = &v1alpha1.PromotionStepLive{Renders: []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Failed",
		Message: "rendered branch env/test was changed outside kardinal: x.yaml changed"}}}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		WithObjects(step, p, makeBundle("b1", "web")).Build()
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &gitNeverCalled{mockGit{cloneErr: assert.AnError}},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	key := types.NamespacedName{Name: step.Name, Namespace: "default"}
	var got v1alpha1.PromotionStep
	for range 4 {
		_, _ = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		require.NoError(t, c.Get(ctx, key, &got))
		if got.Status.State == "Failed" {
			break
		}
	}
	assert.Equal(t, "Failed", got.Status.State)
	assert.Contains(t, got.Status.Message, "render failed (RenderRun rr): rendered branch env/test was changed outside kardinal")
}

// TestRenderStep_KeepsTheRenderedCommit (#1669 with #1515): the commit a
// render step reports, the one it pushed or the rendered branch head when
// it pushed nothing, is the commit the health check waits for. The
// controller does not replace it with the head of a clone in the step's
// work directory (a layout: branch step has none): the git client here
// would report another commit.
func TestRenderStep_KeepsTheRenderedCommit(t *testing.T) {
	const rendered = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		name      string
		noChanges bool
	}{
		{name: "pushed a render"},
		{name: "branch already held the render", noChanges: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			p := makePipeline("web")
			p.Spec.Environments[0].Layout = "branch"
			b := makeBundle("b1", "web")
			step := makeStep("web-b1-test", "web", "b1", "test")
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
				WithObjects(step, p, b).Build()
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{},
				GitClient: &headGit{sha: "ffffffffffffffffffffffffffffffffffffffff"},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}
			key := types.NamespacedName{Name: step.Name, Namespace: "default"}
			reconcile := func() *v1alpha1.PromotionStep {
				t.Helper()
				_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
				require.NoError(t, err)
				var got v1alpha1.PromotionStep
				require.NoError(t, c.Get(ctx, key, &got))
				return &got
			}
			reconcile() // Pending → Promoting
			got := reconcile()
			require.NotNil(t, got.Status.RenderRequestedAt)
			got.Spec.Live = &v1alpha1.PromotionStepLive{Renders: []v1alpha1.LiveRenderRun{{Name: "rr", Phase: "Succeeded",
				Result: &v1alpha1.RenderRunResult{CommitSHA: rendered, Branch: "env/test", NoChanges: tc.noChanges,
					DryCommit: "d", Renderer: "kustomize", Objects: 2}}}}
			require.NoError(t, c.Update(ctx, got))
			got = reconcile()
			require.Equal(t, "HealthChecking", got.Status.State, got.Status.Message)
			assert.Equal(t, rendered, got.Status.Outputs["commitSHA"], "the render's commit, not the clone head")
		})
	}
}
