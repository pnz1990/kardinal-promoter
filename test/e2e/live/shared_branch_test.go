//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// sharedPipelines is how many Pipelines TestPipeline_SharedBranchWriters
// runs on one repository and branch, and sharedBundles how many Bundles
// each gets.
const (
	sharedPipelines = 10
	sharedBundles   = 6
)

// sharedPipeline is Pipeline name on repo with environments <name>-test
// (auto) and <name>-prod (pr-review), each at its own path, with Argo CD
// health on its own Application.
func sharedPipeline(ns, name string, repo gitserver.Repo) *v1alpha1.Pipeline {
	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: repo.CloneURL, Branch: repo.Branch,
			SecretRef: &v1alpha1.SecretRef{Name: framework.GitSecretName}}},
	}
	for _, env := range []string{name + "-test", name + "-prod"} {
		approval := "auto"
		if strings.HasSuffix(env, "-prod") {
			approval = "pr-review"
		}
		p.Spec.Environments = append(p.Spec.Environments, v1alpha1.EnvironmentSpec{
			Name: env, Path: fixtures.Path(env), Approval: approval,
			Update: v1alpha1.UpdateConfig{Strategy: "kustomize"},
			Health: v1alpha1.HealthConfig{Type: "argocd", Timeout: "5m",
				ArgoCD: &v1alpha1.HealthTargetRef{Name: ns + "-" + env, Namespace: framework.ArgoCDNamespace}},
		})
	}
	return p
}

// TestPipeline_SharedBranchWriters runs ten Pipelines on one repository and
// branch, each with an auto environment and a pr-review environment at its
// own paths, and creates sixty Bundles across them at once while another
// writer keeps committing to the branch. Every Pipeline's newest Bundle
// reaches its auto environment (git-push rebases onto the commits that moved
// the branch; at least one step reports the rebase), every pr-review
// PR merges without a conflict, every environment file on the branch ends at
// its Pipeline's newest Bundle and every commit of the other writer is on the
// branch (no lost update, nothing force-pushed), and no Pipeline reports
// PathConflict.
//
// Covers PIPE-SHARED-01, PIPE-SHARED-02.
func TestPipeline_SharedBranchWriters(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	ctx := context.Background()

	var envs []string
	names := make([]string, sharedPipelines)
	for i := range names {
		names[i] = fmt.Sprintf("p%02d", i)
		envs = append(envs, names[i]+"-test", names[i]+"-prod")
	}
	repo := e.Repo(t, ns, fixtures.KustomizeRepoFor(envs, fixtures.Workload, func(string) string { return ns }))
	for _, env := range envs {
		e.ArgoApp(t, ns+"-"+env, repo, fixtures.Path(env), ns)
	}
	for _, env := range envs {
		e.WaitArgoApp(t, ns+"-"+env, syncTimeout)
	}
	for _, name := range names {
		require.NoError(t, e.Client.Create(ctx, sharedPipeline(ns, name, repo)))
	}
	before, err := gitserver.Commits(ctx, e.Git, repo, repo.Branch, 1)
	require.NoError(t, err)

	// Another writer (CI, a person, a second controller) commits to the
	// branch every few hundred milliseconds while the promotions run, at
	// paths no Pipeline writes. The controller serializes its own pushes,
	// so this is what makes the branch move between a step's clone and its
	// push.
	git := committer(t, e)
	stop := make(chan struct{})
	writerDone := make(chan []string)
	go func() {
		var shas []string
		defer func() { writerDone <- shas }()
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			case <-time.After(time.Duration(150+rand.IntN(250)) * time.Millisecond):
			}
			sha, err := git.CommitFiles(ctx, repo, fmt.Sprintf("ci note %d", n),
				map[string][]byte{fmt.Sprintf("notes/ci-%04d.txt", n): []byte("note\n")})
			if err == nil {
				shas = append(shas, sha)
			}
		}
	}()

	// Sixty Bundles at once: each Pipeline alternates V2 and V3 and ends at V3.
	a := &app{e: e, ns: ns, repo: repo}
	newest := make([]string, sharedPipelines)
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range sharedBundles {
				tag := fixtures.V2
				if b%2 == 1 {
					tag = fixtures.V3
				}
				newest[i] = a.createBundle(t, v1alpha1.BundleSpec{Pipeline: name, Images: podinfoImages(tag)})
				time.Sleep(time.Duration(rand.IntN(400)) * time.Millisecond)
			}
		}()
	}
	wg.Wait()

	rebased := 0
	for i, name := range names {
		e.WaitStepState(t, ns, name, newest[i], name+"-test", "Verified", 10*time.Minute)
	}
	close(stop)
	notes := <-writerDone
	require.NotEmpty(t, notes, "the other writer committed")
	// Every step that pushed to the test environments, superseded or not.
	var all v1alpha1.PromotionStepList
	require.NoError(t, e.Client.List(ctx, &all))
	for _, ps := range all.Items {
		if ps.Namespace != ns || !strings.HasSuffix(ps.Spec.Environment, "-test") {
			continue
		}
		if n, err := strconv.Atoi(ps.Status.Outputs["rebases"]); err == nil {
			rebased += n
		}
	}
	assert.Positive(t, rebased, "auto environments of different Pipelines pushed into each other and rebased")

	// The base branch moves while the prod PRs wait: every PR branch is
	// rebuilt on the new head (status.outputs.baseSHA follows it).
	for i, name := range names {
		e.WaitStepState(t, ns, name, newest[i], name+"-prod", "WaitingForMerge", 10*time.Minute)
	}
	moved, err := git.CommitFiles(ctx, repo, "ci note while the PRs wait",
		map[string][]byte{"notes/ci-waiting.txt": []byte("note\n")})
	require.NoError(t, err)
	notes = append(notes, moved)
	for i, name := range names {
		framework.Eventually(t, 3*time.Minute, name+"-prod PR branch rebuilt on the moved base", func(ctx context.Context) (bool, string) {
			ps, ok, err := e.Step(ctx, ns, name, newest[i], name+"-prod")
			if err != nil || !ok {
				return false, fmt.Sprintf("step: %v", err)
			}
			return ps.Status.Outputs["baseSHA"] == moved, fmt.Sprintf("baseSHA %s rebuilds %s: %s",
				ps.Status.Outputs["baseSHA"], ps.Status.Outputs["prBranchRebuilds"], ps.Status.Message)
		})
	}

	// Every prod PR merges, one after the other, each onto a branch the
	// others moved: no conflicts between Pipelines.
	for i, name := range names {
		a.merge(t, a.openPR(t, newest[i], name+"-prod"))
	}
	for i, name := range names {
		e.WaitStepState(t, ns, name, newest[i], name+"-prod", "Verified", 10*time.Minute)
	}

	for _, env := range envs {
		a.fileHas(t, env, fixtures.V3, env+": the newest Bundle's tag is on the branch")
		a.running(t, env, fixtures.Image+":"+fixtures.V3, env)
	}
	prs, err := e.Git.PullRequests(ctx, repo)
	require.NoError(t, err)
	for _, pr := range prs {
		assert.NotEqual(t, "open", pr.State, "PR #%d %s is still open", pr.Number, pr.Head)
	}
	commits, err := gitserver.Commits(ctx, e.Git, repo, repo.Branch, 500)
	require.NoError(t, err)
	require.NotEmpty(t, before)
	found := false
	for _, c := range commits {
		if c.SHA == before[0].SHA {
			found = true
		}
	}
	assert.True(t, found, "the commit the branch started at is still in its history: nothing was force-pushed")
	files := map[string]bool{}
	for _, c := range commits {
		files[c.SHA] = true
	}
	for _, sha := range notes {
		assert.True(t, files[sha], "the other writer's commit %s is on the branch", sha)
	}
	for _, name := range names {
		var p v1alpha1.Pipeline
		require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &p))
		assert.Nil(t, meta.FindStatusCondition(p.Status.Conditions, "PathConflict"), name)
	}
	t.Logf("%d git-push steps rebased onto other Pipelines' commits", rebased)
}

// TestPipeline_PathConflict creates two Pipelines on one repository and
// branch whose environments share a path: both report PathConflict naming
// the other; moving one to its own path clears the condition on both.
//
// Covers PIPE-SHARED-03.
func TestPipeline_PathConflict(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	ctx := context.Background()
	repo := e.Repo(t, ns, fixtures.KustomizeRepoFor([]string{"api-test", "api-prod"}, fixtures.Workload,
		func(string) string { return ns }))
	api := sharedPipeline(ns, "api", repo)
	web := sharedPipeline(ns, "web", repo)
	web.Spec.Environments[0].Path = api.Spec.Environments[0].Path // web-test writes api-test's directory
	api.Spec.Paused, web.Spec.Paused = true, true
	require.NoError(t, e.Client.Create(ctx, api))
	require.NoError(t, e.Client.Create(ctx, web))

	cond := func(name string) *metav1.Condition {
		var p v1alpha1.Pipeline
		require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &p))
		return meta.FindStatusCondition(p.Status.Conditions, "PathConflict")
	}
	framework.Eventually(t, time.Minute, "PathConflict on both Pipelines", func(context.Context) (bool, string) {
		a, w := cond("api"), cond("web")
		return a != nil && w != nil, fmt.Sprintf("api=%v web=%v", a, w)
	})
	assert.Equal(t, "OverlappingPath", cond("api").Reason)
	assert.Contains(t, cond("api").Message, fmt.Sprintf("environment api-test (%s) and Pipeline web environment web-test (%s)",
		fixtures.Path("api-test"), fixtures.Path("api-test")))
	a := &app{e: e, ns: ns, repo: repo}
	a.updatePipeline(t, "web", func(p *v1alpha1.Pipeline) { p.Spec.Environments[0].Path = fixtures.Path("web-test") })
	framework.Eventually(t, time.Minute, "PathConflict cleared on both", func(context.Context) (bool, string) {
		a, w := cond("api"), cond("web")
		return a == nil && w == nil, fmt.Sprintf("api=%v web=%v", a, w)
	})
}
