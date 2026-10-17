// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// growingBranch is a git host whose branches are chains of commits
// <branch>-<i>, each changing notes/<i> (other environments' files); it
// counts the branch-head reads.
type growingBranch struct {
	scm.GitClient
	mu        sync.Mutex
	n         map[string]int
	headReads int
}

func (g *growingBranch) sha(branch string, i int) string { return fmt.Sprintf("%s-%07d", branch, i) }

func (g *growingBranch) push(branch string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n[branch]++
	return g.sha(branch, g.n[branch]-1)
}

func (g *growingBranch) RemoteHeads(context.Context, string, scm.GitAuth) (map[string]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.headReads++
	heads := map[string]string{}
	for b, n := range g.n {
		heads[b] = g.sha(b, n-1)
	}
	return heads, nil
}

func (g *growingBranch) BranchGraph(_ context.Context, _, branch string, _ scm.GitAuth, max int) (string, map[string]scm.GraphCommit, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := g.n[branch]
	out := map[string]scm.GraphCommit{}
	for i := n - 1; i >= 0 && len(out) < max; i-- {
		gc := scm.GraphCommit{Paths: []string{fmt.Sprintf("notes/%d", i)}}
		if i > 0 {
			gc.Parents = []string{g.sha(branch, i-1)}
		}
		out[g.sha(branch, i)] = gc
	}
	return g.sha(branch, n-1), out, nil
}

func (g *growingBranch) BranchHistory(context.Context, string, string, scm.GitAuth, int) ([]scm.CommitPaths, error) {
	return nil, nil
}

// TestBranchMoved_StaleCachedHead (#2137 live: TestGraph_CompactShape300):
// the shared cache's heads were read before the step pushed, so the cached
// head does not hold the step's commit. The check reads the heads again,
// once, and passes; it does not wait on a head older than its own push.
func TestBranchMoved_StaleCachedHead(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	g := &growingBranch{n: map[string]int{"main": 10}}
	r := &Reconciler{GitClient: g, NowFn: func() time.Time { return now }}
	p := &v1alpha1.Pipeline{Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: "file:///repo"}}}
	env := v1alpha1.EnvironmentSpec{Name: "w29-r0", Path: "environments/w29-r0"}

	_, err := r.remotes.remoteHeads(ctx, g, "file:///repo", scm.GitAuth{}, now) // another step's read
	require.NoError(t, err)
	want := g.push("main") // this step's push
	g.push("main")         // and another environment's, after it

	assert.Empty(t, r.branchMoved(ctx, zerolog.Nop(), p, env, want))
	assert.Equal(t, 2, g.headReads, "the stale heads are read again once")
	assert.Empty(t, r.branchMoved(ctx, zerolog.Nop(), p, env, want))
	assert.Equal(t, 2, g.headReads, "the fresh heads answer the next check from the cache")
}

// TestBranchMoved_RenderedBranch (#2137 live: TestGraph_FleetRenderedBranches):
// a layout: branch environment's step pushes its rendered branch, not the
// base branch; the check reads that branch.
func TestBranchMoved_RenderedBranch(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	g := &growingBranch{n: map[string]int{"main": 5, "env/prod-t00": 2}}
	r := &Reconciler{GitClient: g, NowFn: func() time.Time { return now }}
	p := &v1alpha1.Pipeline{Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: "file:///repo"}}}
	env := v1alpha1.EnvironmentSpec{Name: "prod-t00", Layout: "branch"}

	want := g.push("env/prod-t00")
	assert.Empty(t, r.branchMoved(ctx, zerolog.Nop(), p, env, want))
	g.push("env/prod-t00") // a later render of the environment replaced it
	now = now.Add(remoteHeadsTTL)
	assert.Contains(t, r.branchMoved(ctx, zerolog.Nop(), p, env, want), "branch env/prod-t00 no longer holds")
}
