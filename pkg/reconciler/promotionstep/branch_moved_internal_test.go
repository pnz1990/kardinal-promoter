// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// growingBranch is a git host whose branches are chains of commits
// <branch>-<i>, each changing notes/<i> (other environments' files) unless
// pushed with a path; it counts the branch-head reads, and fails them once
// failHeads is set.
type growingBranch struct {
	scm.GitClient
	mu        sync.Mutex
	n         map[string]int
	paths     map[string]string // commit -> the path it changed
	headReads int
	failHeads error
}

func (g *growingBranch) sha(branch string, i int) string { return fmt.Sprintf("%s-%07d", branch, i) }

func (g *growingBranch) push(branch string) string { return g.pushPath(branch, "") }

// pushPath adds a commit to branch that changes path ("": notes/<i>).
func (g *growingBranch) pushPath(branch, path string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n[branch]++
	sha := g.sha(branch, g.n[branch]-1)
	if path != "" {
		if g.paths == nil {
			g.paths = map[string]string{}
		}
		g.paths[sha] = path
	}
	return sha
}

func (g *growingBranch) RemoteHeads(context.Context, string, scm.GitAuth) (map[string]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.headReads++
	if g.failHeads != nil {
		return nil, g.failHeads
	}
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
		sha := g.sha(branch, i)
		path := g.paths[sha]
		if path == "" {
			path = fmt.Sprintf("notes/%d", i)
		}
		gc := scm.GraphCommit{Paths: []string{path}}
		if i > 0 {
			gc.Parents = []string{g.sha(branch, i-1)}
		}
		out[sha] = gc
	}
	return g.sha(branch, n-1), out, nil
}

func (g *growingBranch) BranchHistory(context.Context, string, string, scm.GitAuth, int) ([]scm.CommitPaths, error) {
	return nil, nil
}

// movedWorld is a Reconciler on g at a clock the test moves, and the
// Pipeline and environment of its checks.
func movedWorld(g *growingBranch) (*Reconciler, *time.Time, *v1alpha1.Pipeline) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	r := &Reconciler{GitClient: g, NowFn: func() time.Time { return now }}
	return r, &now, &v1alpha1.Pipeline{Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: "file:///repo"}}}
}

var w29 = v1alpha1.EnvironmentSpec{Name: "w29-r0", Path: "environments/w29-r0"}

// TestBranchMoved_StaleCachedHead (#2137 live: TestGraph_CompactShape300):
// the shared cache's heads were read before the step pushed, so they do not
// hold its commit. The check reads the heads again, once, and passes; it does
// not wait on heads older than its own push.
func TestBranchMoved_StaleCachedHead(t *testing.T) {
	ctx := context.Background()
	g := &growingBranch{n: map[string]int{"main": 10}}
	r, now, p := movedWorld(g)
	_, err := r.remotes.remoteHeads(ctx, g, "file:///repo", scm.GitAuth{}, *now) // another step's read
	require.NoError(t, err)
	want := g.push("main") // this step's push, in the same second
	landed := *now
	g.push("main") // and another environment's, after it
	*now = now.Add(2 * time.Second)

	assert.Empty(t, r.branchMoved(ctx, zerolog.Nop(), p, w29, want, landed))
	assert.Equal(t, 2, g.headReads, "the heads read before the push are read again once")
	assert.Empty(t, r.branchMoved(ctx, zerolog.Nop(), p, w29, want, landed))
	assert.Equal(t, 2, g.headReads, "heads read after the push answer the next check from the cache")
}

// TestBranchMoved_OutOfBandCommitAfterCachedHeads (#2153 QA, coordinator's
// call): the cache's heads were read just after the step's push, in the same
// second, and show its commit; a person then changes the environment's files.
// Heads not read after the push landed are not trusted: they are read again,
// and the check does not pass.
func TestBranchMoved_OutOfBandCommitAfterCachedHeads(t *testing.T) {
	ctx := context.Background()
	g := &growingBranch{n: map[string]int{"main": 10}}
	r, now, p := movedWorld(g)
	want := g.push("main")
	landed := *now
	_, err := r.remotes.remoteHeads(ctx, g, "file:///repo", scm.GitAuth{}, *now) // head == want
	require.NoError(t, err)
	g.pushPath("main", "environments/w29-r0/kustomization.yaml") // a hand edit
	*now = now.Add(2 * time.Second)

	assert.Contains(t, r.branchMoved(ctx, zerolog.Nop(), p, w29, want, landed), "no longer holds")
}

// TestBranchMoved_FreshReadFails (#2153 QA): the cached heads are readable but
// older than the step's push, and the read again fails: the check waits,
// saying why; it never passes on the stale heads.
func TestBranchMoved_FreshReadFails(t *testing.T) {
	ctx := context.Background()
	g := &growingBranch{n: map[string]int{"main": 10}}
	r, now, p := movedWorld(g)
	want := g.push("main")
	landed := *now
	_, err := r.remotes.remoteHeads(ctx, g, "file:///repo", scm.GitAuth{}, *now) // head == want
	require.NoError(t, err)
	g.failHeads = errors.New("HTTP 503")
	*now = now.Add(2 * time.Second)

	why := r.branchMoved(ctx, zerolog.Nop(), p, w29, want, landed)
	assert.Contains(t, why, "could not be read")
	assert.Contains(t, why, "HTTP 503")
}

// TestBranchMoved_RenderedBranch (#2137 live: TestGraph_FleetRenderedBranches):
// a layout: branch environment's step pushes its rendered branch, not the
// base branch; the check reads that branch.
func TestBranchMoved_RenderedBranch(t *testing.T) {
	ctx := context.Background()
	g := &growingBranch{n: map[string]int{"main": 5, "env/prod-t00": 2}}
	env := v1alpha1.EnvironmentSpec{Name: "prod-t00", Layout: "branch"}
	r, now, p := movedWorld(g)
	want := g.push("env/prod-t00")
	landed := *now
	*now = now.Add(2 * time.Second)
	assert.Empty(t, r.branchMoved(ctx, zerolog.Nop(), p, env, want, landed))
	g.push("env/prod-t00") // a later render of the environment replaced it
	*now = now.Add(remoteHeadsTTL)
	assert.Contains(t, r.branchMoved(ctx, zerolog.Nop(), p, env, want, landed), "branch env/prod-t00 no longer holds")
}

// TestStepMoved_LandedFromTheStep (#2153 QA): the health check's call takes
// landed from the step. Its git-push completed at T; the shared cache holds
// heads read at T-5s whose head is the step's commit; the remote has since
// a hand edit of the environment's files. The cached heads predate the
// commit's landing, so they are read again, and the check does not pass. A
// step with no completed step waits: a zero landed fails closed.
func TestStepMoved_LandedFromTheStep(t *testing.T) {
	ctx := context.Background()
	g := &growingBranch{n: map[string]int{"main": 10}}
	r, now, p := movedWorld(g)
	want := g.push("main")
	_, err := r.remotes.remoteHeads(ctx, g, "file:///repo", scm.GitAuth{}, now.Add(-5*time.Second)) // head == want
	require.NoError(t, err)
	g.pushPath("main", "environments/w29-r0/kustomization.yaml") // a hand edit
	pushed := metav1.NewTime(*now)
	ps := &v1alpha1.PromotionStep{Status: v1alpha1.PromotionStepStatus{Steps: []v1alpha1.StepStatus{
		{Name: "git-push", State: v1alpha1.StepExecutionCompleted, CompletedAt: &pushed}}}}
	*now = now.Add(2 * time.Second)
	assert.Contains(t, r.stepMoved(ctx, zerolog.Nop(), p, w29, ps, want), "no longer holds")

	none := &v1alpha1.PromotionStep{Status: v1alpha1.PromotionStepStatus{Steps: []v1alpha1.StepStatus{{Name: "git-push"}}}}
	assert.Contains(t, r.stepMoved(ctx, zerolog.Nop(), p, w29, none, want), "has not landed yet")
}

// TestStepsLanded (#2153 QA): the latest CompletedAt of the step's steps
// (wait-for-merge after git-push), zero when none completed.
func TestStepsLanded(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *metav1.Time { v := metav1.NewTime(t0.Add(d)); return &v }
	for _, tc := range []struct {
		name  string
		steps []v1alpha1.StepStatus
		want  time.Time
	}{
		{"none completed", []v1alpha1.StepStatus{{Name: "git-clone"}, {Name: "git-push"}}, time.Time{}},
		{"the push", []v1alpha1.StepStatus{{Name: "git-clone", CompletedAt: at(0)}, {Name: "git-push", CompletedAt: at(time.Second)}},
			t0.Add(time.Second)},
		{"the merge, after the push", []v1alpha1.StepStatus{{Name: "git-push", CompletedAt: at(time.Second)},
			{Name: "wait-for-merge", CompletedAt: at(time.Hour)}, {Name: "health-check"}}, t0.Add(time.Hour)},
		{"the latest, whatever the order", []v1alpha1.StepStatus{{Name: "wait-for-merge", CompletedAt: at(time.Hour)},
			{Name: "git-push", CompletedAt: at(time.Second)}}, t0.Add(time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := &v1alpha1.PromotionStep{Status: v1alpha1.PromotionStepStatus{Steps: tc.steps}}
			assert.True(t, tc.want.Equal(stepsLanded(ps)), "got %s", stepsLanded(ps))
		})
	}
}
