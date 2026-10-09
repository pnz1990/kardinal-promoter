// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// countingGit is mockGit that counts clones and pushes.
type countingGit struct {
	mockGit
	clones, pushes atomic.Int32
}

func (g *countingGit) Clone(ctx context.Context, a, b, c, d string) error {
	g.clones.Add(1)
	return g.mockGit.Clone(ctx, a, b, c, d)
}

func (g *countingGit) Push(ctx context.Context, a, b, c, d string, f bool) error {
	g.pushes.Add(1)
	return g.mockGit.Push(ctx, a, b, c, d, f)
}

// TestBranchTurn_WaitingStepDoesNoGitWork (#1577, #1578): an auto step whose
// base branch another promotion of this controller is pushing to does no git
// work. It is requeued at low priority with a short wait, its message says
// why, and no retry is spent. Once the turn is free it clones, commits and
// pushes, and its next requeue is at normal priority again.
//
// Covers PERF-PUSH-QUEUE-01.
func TestBranchTurn_WaitingStepDoesNoGitWork(t *testing.T) {
	pipeline := makePipeline("nginx-demo")
	ps := asPromoting(makeStep("step-a", "nginx-demo", "b1", "test"), pipeline)
	c := newClient(t, ps, pipeline, makeBundle("b1", "nginx-demo"))
	git := &countingGit{}
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: git,
		HealthDetector: health.NewAutoDetector(c, dynfake.NewSimpleDynamicClient(runtime.NewScheme())),
		WorkDirFn:      func(_, _ string) string { return t.TempDir() }}

	release := promotionstep.HoldBranchTurn(r, "https://github.com/test/repo", "main", "default/other-step")
	res, err := r.Reconcile(context.Background(), reqFor("step-a"))
	require.NoError(t, err)
	assert.Positive(t, res.RequeueAfter)
	require.NotNil(t, res.Priority)
	assert.Negative(t, *res.Priority, "waits at low priority: other steps go first")
	assert.Zero(t, git.clones.Load(), "no git work while another promotion pushes the branch")
	got := getStep(t, c, "step-a")
	assert.Equal(t, "Promoting", got.Status.State)
	assert.Contains(t, got.Status.Message, "waiting for its turn to push to main")
	assert.Zero(t, got.Status.RetryCount)

	rv := got.ResourceVersion
	_, err = r.Reconcile(context.Background(), reqFor("step-a"))
	require.NoError(t, err)
	assert.Equal(t, rv, getStep(t, c, "step-a").ResourceVersion, "waiting again writes no status")

	release()
	res, err = r.Reconcile(context.Background(), reqFor("step-a"))
	require.NoError(t, err)
	assert.Equal(t, int32(1), git.clones.Load())
	assert.Equal(t, int32(1), git.pushes.Load())
	assert.Equal(t, "HealthChecking", getStep(t, c, "step-a").Status.State)
	require.NotNil(t, res.Priority)
	assert.Zero(t, *res.Priority, "with its turn taken, back at normal priority")
}
