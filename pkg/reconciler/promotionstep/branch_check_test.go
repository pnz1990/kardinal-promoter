// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// chainGit is a remote whose branch main is a chain of commits, each
// changing one path; it answers the controller's head and graph reads.
type chainGit struct {
	mockGit
	mu      sync.Mutex
	commits []string          // oldest first
	paths   map[string]string // commit -> the path it changed
}

func (g *chainGit) add(sha, path string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.commits = append(g.commits, sha)
	g.paths[sha] = path
}

func (g *chainGit) RemoteHeads(context.Context, string, scm.GitAuth) (map[string]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return map[string]string{"main": g.commits[len(g.commits)-1]}, nil
}

func (g *chainGit) BranchGraph(_ context.Context, _, _ string, _ scm.GitAuth, max int) (string, map[string]scm.GraphCommit, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]scm.GraphCommit{}
	for i := len(g.commits) - 1; i >= 0 && len(out) < max; i-- {
		gc := scm.GraphCommit{Paths: []string{g.paths[g.commits[i]]}}
		if i > 0 {
			gc.Parents = []string{g.commits[i-1]}
		}
		out[g.commits[i]] = gc
	}
	return g.commits[len(g.commits)-1], out, nil
}

func (g *chainGit) BranchHistory(context.Context, string, string, scm.GitAuth, int) ([]scm.CommitPaths, error) {
	return nil, nil
}

// TestHealthCheck_TrustsOnlyHeadsReadAfterTheCommitLanded (#2153 QA): through
// the health check itself. Two environments of one branch; images that do
// not name the version, so the resource check also requires the branch to
// hold the step's commit. Step a's check at T-5s fills the shared cache with
// heads whose head is step b's commit. Step b's git-push completed at T; a
// hand edit of b's files follows. b's check at T+2s must not pass on the
// cached heads: it reads them again and waits. Without the edit it is
// Verified (the landing time is wired through; a zero one would wait).
func TestHealthCheck_TrustsOnlyHeadsReadAfterTheCommitLanded(t *testing.T) {
	for _, tc := range []struct {
		name      string
		handEdit  bool
		wantState string
		wantMsg   string
	}{
		{"a hand edit after the cached read: not Verified", true, "HealthChecking", "branch main no longer holds"},
		{"no edit: Verified", false, "Verified", "image not verified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			T := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
			now := T.Add(-5 * time.Second)
			pl := makePipeline("p")
			pl.Spec.Environments = nil
			for _, n := range []string{"a", "b"} {
				pl.Spec.Environments = append(pl.Spec.Environments, v1alpha1.EnvironmentSpec{Name: n, Approval: "auto",
					Path: "environments/" + n, Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "1h"}})
			}
			bundle := makeBundle("b1", "p")
			bundle.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2"}}
			step := func(env, sha string, pushed time.Time) *v1alpha1.PromotionStep {
				ps := labelled(makeStep("step-"+env, "p", "b1", env))
				ps.Status.State = "HealthChecking"
				ps.Status.Steps = recordedSteps("auto")
				at := metav1.NewTime(pushed)
				for i := range ps.Status.Steps {
					ps.Status.Steps[i].CompletedAt = &at
				}
				hc := metav1.NewTime(pushed)
				ps.Status.Steps = append(ps.Status.Steps, v1alpha1.StepStatus{Name: "health-check",
					State: v1alpha1.StepExecutionInProgress, StartedAt: &hc})
				ps.Status.Outputs = map[string]string{"commitSHA": sha}
				return ps
			}
			// The workloads run an image named otherwise: availability alone.
			objs := []client.Object{pl, bundle, step("a", "c-a", T.Add(-10*time.Second)), step("b", "c-b", T),
				withImage(healthyDeployment("p", "a"), "ghcr.io/org/renamed:v2"),
				withImage(healthyDeployment("p", "b"), "ghcr.io/org/renamed:v2")}
			c := newClient(t, objs...)
			git := &chainGit{paths: map[string]string{}}
			git.add("c-0", "README.md")
			git.add("c-a", "environments/a/kustomization.yaml")
			git.add("c-b", "environments/b/kustomization.yaml")
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: git,
				HealthDetector: health.NewAutoDetector(c, dynfake.NewSimpleDynamicClient(runtime.NewScheme())),
				NowFn:          func() time.Time { return now }}

			_, err := r.Reconcile(context.Background(), reqFor("step-a")) // fills the cache at T-5s: head c-b
			require.NoError(t, err)
			require.Equal(t, "Verified", getStep(t, c, "step-a").Status.State, getStep(t, c, "step-a").Status.Message)

			if tc.handEdit {
				git.add("c-hand", "environments/b/kustomization.yaml")
			}
			now = T.Add(2 * time.Second)
			_, err = r.Reconcile(context.Background(), reqFor("step-b"))
			require.NoError(t, err)
			got := getStep(t, c, "step-b")
			assert.Equal(t, tc.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tc.wantMsg)
		})
	}
}
