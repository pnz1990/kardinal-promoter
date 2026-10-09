// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
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

// remoteGit is a git server with one branch that refuses a push from a
// checkout cloned before the branch last moved, as a real one refuses a
// non-fast-forward push. It counts landed and refused pushes.
type remoteGit struct {
	mockGit
	mu               sync.Mutex
	head             int
	cloned           map[string]int
	landed, refused  int
	clones, inFlight int
	maxInFlight      int
	// nextClone is closed by the next Clone (barrier).
	nextClone chan struct{}
}

// cloneBarrier is how long a Clone waits for another checkout to be cloned
// before it returns (remoteGit.Clone).
const cloneBarrier = 100 * time.Millisecond

// Clone records the checkout, then waits until another one is cloned or
// cloneBarrier passes: steps that clone together all hold a checkout of the
// same head before any pushes, so without turns all but one push is refused
// every time, not only when the scheduler happens to interleave them.
func (g *remoteGit) Clone(ctx context.Context, _, _, dir, _ string) error {
	g.mu.Lock()
	g.cloned[dir] = g.head
	g.clones++
	g.inFlight++
	g.maxInFlight = max(g.maxInFlight, g.inFlight)
	if g.nextClone != nil {
		close(g.nextClone)
	}
	next := make(chan struct{})
	g.nextClone = next
	g.mu.Unlock()
	select {
	case <-next:
	case <-time.After(cloneBarrier):
	case <-ctx.Done():
	}
	return nil
}

func (g *remoteGit) Push(_ context.Context, dir, _, _, _ string, _ bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inFlight--
	if g.cloned[dir] != g.head {
		g.refused++
		return fmt.Errorf("push: %w", scm.ErrNonFastForward)
	}
	g.head++
	g.landed++
	return nil
}

// TestBranchTurn_ConcurrentWave (#1578 QA): N auto promotions of N Pipelines
// writing one branch, reconciled concurrently as the controller's workers
// do, against a server that refuses stale pushes: every one lands with N
// pushes in all and none refused, and never more than one checkout is
// between clone and push at a time. Without turns each step raced the
// others and most pushes were refused; the server's clone barrier makes
// that certain, so the test fails whenever turns are off.
//
// Covers PERF-PUSH-QUEUE-01.
func TestBranchTurn_ConcurrentWave(t *testing.T) {
	const n = 12
	var objs []client.Object
	for i := 0; i < n; i++ {
		p := makePipeline(fmt.Sprintf("p%02d", i))
		b := makeBundle(fmt.Sprintf("b%02d", i), p.Name)
		ps := asPromoting(makeStep(fmt.Sprintf("s%02d", i), p.Name, b.Name, "test"), p)
		objs = append(objs, p, b, ps)
	}
	c := newClient(t, objs...)
	git := &remoteGit{cloned: map[string]int{}}
	root := t.TempDir()
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: git,
		HealthDetector: health.NewAutoDetector(c, dynfake.NewSimpleDynamicClient(runtime.NewScheme())),
		WorkDirFn:      func(p, b string) string { return filepath.Join(root, p, b) }}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			for ctx.Err() == nil {
				res, err := r.Reconcile(ctx, reqFor(name))
				if err != nil {
					t.Errorf("%s: %v", name, err)
					return
				}
				var ps v1alpha1.PromotionStep
				if err := c.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, &ps); err != nil {
					t.Errorf("%s: %v", name, err)
					return
				}
				if ps.Status.State != "Promoting" {
					return
				}
				time.Sleep(min(res.RequeueAfter, 20*time.Millisecond))
			}
		}(fmt.Sprintf("s%02d", i))
	}
	wg.Wait()
	require.NoError(t, ctx.Err(), "every step left Promoting")
	assert.Equal(t, n, git.landed, "one push per environment")
	assert.Zero(t, git.refused, "no push refused as non-fast-forward")
	assert.Equal(t, n, git.clones, "one clone per environment")
	assert.Equal(t, 1, git.maxInFlight, "one checkout between clone and push at a time")
	for i := 0; i < n; i++ {
		assert.Equal(t, "HealthChecking", getStep(t, c, fmt.Sprintf("s%02d", i)).Status.State)
	}
}

// TestBranchTurn_NextStepTakesTurnAfterPush (#1578): the turn ends once
// git-push has landed, not when the reconcile does. A second step asking
// for the branch takes it, clones and pushes while the first is still in
// the reconcile that pushed (here: in its status write). Were the turn held
// to the end of the reconcile, the second step would be told to wait.
//
// Covers PERF-PUSH-QUEUE-01.
func TestBranchTurn_NextStepTakesTurnAfterPush(t *testing.T) {
	pa, pb := makePipeline("p-a"), makePipeline("p-b")
	ba, bb := makeBundle("b-a", "p-a"), makeBundle("b-b", "p-b")
	sa, sb := asPromoting(makeStep("s-a", "p-a", "b-a", "test"), pa), asPromoting(makeStep("s-b", "p-b", "b-b", "test"), pb)
	git := &remoteGit{cloned: map[string]int{}}
	root := t.TempDir()
	var r *promotionstep.Reconciler
	var duringA struct {
		ran     bool
		bClones int
		bState  string
		bErr    error
	}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}, &v1alpha1.Bundle{}).
		WithObjects(pa, pb, ba, bb, sa, sb).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, cl client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				git.mu.Lock()
				aPushed := git.landed > 0
				git.mu.Unlock()
				if obj.GetName() == "s-a" && aPushed && !duringA.ran {
					// s-a has pushed and its reconcile has not ended.
					duringA.ran = true
					_, duringA.bErr = r.Reconcile(ctx, reqFor("s-b"))
					git.mu.Lock()
					duringA.bClones = git.clones - 1
					git.mu.Unlock()
					var b v1alpha1.PromotionStep
					if err := cl.Get(ctx, client.ObjectKey{Namespace: "default", Name: "s-b"}, &b); err == nil {
						duringA.bState = b.Status.State
					}
				}
				return cl.SubResource(sub).Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r = &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: git,
		HealthDetector: health.NewAutoDetector(c, dynfake.NewSimpleDynamicClient(runtime.NewScheme())),
		WorkDirFn:      func(p, b string) string { return filepath.Join(root, p, b) }}

	// s-a takes the turn; s-b asks for it from inside s-a's reconcile.
	_, err := r.Reconcile(context.Background(), reqFor("s-a"))
	require.NoError(t, err)
	require.True(t, duringA.ran, "s-a wrote its status after its push")
	require.NoError(t, duringA.bErr)
	assert.Equal(t, 1, duringA.bClones, "s-b cloned while s-a's reconcile was still running")
	assert.Equal(t, "HealthChecking", duringA.bState, "and pushed")
	assert.Equal(t, 2, git.landed)
	assert.Zero(t, git.refused)
	assert.Equal(t, "HealthChecking", getStep(t, c, "s-a").Status.State)
}
