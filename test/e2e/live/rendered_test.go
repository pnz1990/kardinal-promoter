//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// renderedApp is an app whose environments use layout: branch: the DRY
// source on the repo's branch, and one Argo CD Application per environment
// that syncs the root of its rendered branch env/<env> as plain manifests.
func renderedApp(t *testing.T, e *framework.Env, files func(fixtures.App) map[string][]byte, envs ...string) *app {
	t.Helper()
	ns := e.Namespace(t)
	a := &app{e: e, ns: ns, envs: envs, repo: e.Repo(t, ns, files(fixtures.App{Namespace: ns, Envs: envs}))}
	for _, env := range envs {
		rendered := a.repo
		rendered.Branch = "env/" + env
		e.ArgoApp(t, a.argoApp(env), rendered, ".", ns)
	}
	return a
}

// renderedPipeline is a.pipeline with layout: branch on every environment.
func (a *app) renderedPipeline(approval map[string]string) *v1alpha1.Pipeline {
	p := a.pipeline(approval)
	for i := range p.Spec.Environments {
		p.Spec.Environments[i].Layout = "branch"
	}
	return p
}

// headSHA is the commit at the head of branch.
func headSHA(t *testing.T, e *framework.Env, branch string, r gitserver.Repo) string {
	t.Helper()
	c, err := gitserver.Commits(context.Background(), e.Git, r, branch, 1)
	require.NoError(t, err)
	require.NotEmpty(t, c, "branch %s has commits", branch)
	return c[0].SHA
}

// onBranch is a.repo with its branch set to branch.
func onBranch(r gitserver.Repo, branch string) gitserver.Repo {
	r.Branch = branch
	return r
}

// TestCore_RenderedBranchKustomize promotes with layout: branch from a
// kustomize DRY source into rendered branches that Argo CD syncs:
//   - the first promotion creates env/test, renders the overlay with the
//     Bundle's tag in the controller (no kustomize binary), one file per
//     object plus .kardinal/rendered.yaml, and leaves the DRY source untouched;
//     the rendered commit's trailer names the DRY commit; test runs the release;
//   - prod is pr-review: its PR targets env/prod and its diff is the rendered
//     YAML; once merged, prod runs the release;
//   - a rollback renders the DRY commit of the Bundle it restores;
//   - a direct push to env/test is drift: the next promotion fails and pushes
//     nothing.
//
// Covers REND-KUST-01, REND-PR-01, REND-ROLLBACK-01, REND-DRIFT-01.
func TestCore_RenderedBranchKustomize(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := renderedApp(t, e, fixtures.KustomizeRepo, "test", "prod")
	dryHead := headSHA(t, e, a.repo.Branch, a.repo)
	a.apply(t, a.renderedPipeline(map[string]string{"prod": "pr-review"}))
	deployment := func(env string) string { return a.ns + "_deployment-" + fixtures.Workload(env) + ".yaml" }

	v2 := fixtures.Image + ":" + fixtures.V2
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", v2)
	ps := e.WaitStepState(t, a.ns, pipelineName, b2, "test", "Verified", promoteTimeout)
	checkSteps(t, ps, []string{"git-clone", "kustomize-set-image", "render-manifests", "git-commit", "git-push", "health-check"})
	assert.Equal(t, dryHead, ps.Status.Outputs["dryCommit"], "the head of the DRY source was rendered")
	assert.Contains(t, e.ReadFile(t, a.repo, "env/test", deployment("test")), "image: "+v2)
	assert.Contains(t, e.ReadFile(t, a.repo, "env/test", ".kardinal/rendered.yaml"), "dryCommit: "+dryHead)
	head, err := gitserver.Commits(ctx, e.Git, a.repo, "env/test", 1)
	require.NoError(t, err)
	require.NotEmpty(t, head)
	assert.Contains(t, head[0].Message, "Kardinal-Dry-Commit: "+dryHead)
	assert.Contains(t, head[0].Message, "Kardinal-Bundle: "+b2)
	assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/kustomization.yaml"), "newTag: "+fixtures.V1,
		"the DRY source is not committed to")
	a.running(t, "test", v2, "test runs the rendered release")

	pr := e.WaitPR(t, onBranch(a.repo, "env/prod"), promoteTimeout, "the prod PR into env/prod", func(p gitserver.PR) bool {
		return p.Base == "env/prod" && p.State == "open"
	})
	assert.Contains(t, e.ReadFile(t, a.repo, pr.Head, deployment("prod")), "image: "+v2, "the PR diff is the rendered YAML")
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, b2, "prod", "Verified", promoteTimeout)
	a.running(t, "prod", v2, "prod runs the rendered release after the merge")

	// v3 is rendered from a later DRY commit; the rollback to v2 renders
	// v2's DRY commit again.
	_, err = gitserver.CommitFiles(ctx, e.Git, a.repo, a.repo.Branch, "", "change the DRY source", map[string][]byte{
		"README.md": []byte("DRY change after " + b2 + "\n")})
	require.NoError(t, err)
	v3 := fixtures.Image + ":" + fixtures.V3
	b3 := e.CreateBundle(t, a.ns, pipelineName, "--image", v3)
	ps3 := e.WaitStepState(t, a.ns, pipelineName, b3, "test", "Verified", promoteTimeout)
	require.NotEqual(t, dryHead, ps3.Status.Outputs["dryCommit"])
	a.running(t, "test", v3, "test runs v3")
	_, rb := rbRollback(t, a, "test")
	psRB := e.WaitStepState(t, a.ns, pipelineName, rb, "test", "Verified", promoteTimeout)
	assert.Equal(t, dryHead, psRB.Status.Outputs["dryCommit"], "the rollback renders the DRY commit of %s", b2)
	a.running(t, "test", v2, "test runs v2 again")

	// Drift: someone pushes to env/test directly.
	_, err = gitserver.CommitFiles(ctx, e.Git, a.repo, "env/test", "", "hotfix by hand",
		map[string][]byte{deployment("test"): []byte("edited by hand\n")})
	require.NoError(t, err)
	before := headSHA(t, e, "env/test", a.repo)
	b4 := e.CreateBundle(t, a.ns, pipelineName, "--image", v3)
	ps4 := e.WaitStepState(t, a.ns, pipelineName, b4, "test", "Failed", promoteTimeout)
	assert.Contains(t, ps4.Status.Message, fmt.Sprintf("rendered branch env/test was changed outside kardinal: %s changed", deployment("test")))
	assert.Equal(t, before, headSHA(t, e, "env/test", a.repo), "nothing is pushed to a drifted branch")
}

// TestCore_RenderedBranchHelm renders a Helm chart (helm template in the
// controller) with the values file helm-set-image edits, into env/test, and
// Argo CD syncs the plain manifests: no Chart.yaml or values on the rendered
// branch, and the release runs.
//
// Covers REND-HELM-01.
func TestCore_RenderedBranchHelm(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := renderedApp(t, e, fixtures.HelmRepo, "test")
	p := a.renderedPipeline(nil)
	envSpec(t, p, "test").Update = v1alpha1.UpdateConfig{Strategy: "helm", Helm: &v1alpha1.HelmUpdateConfig{
		ValuesFile: fixtures.HelmValuesFile, ImagePathTemplate: fixtures.HelmImagePath}}
	a.apply(t, p)
	v2 := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", v2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	checkSteps(t, ps, []string{"git-clone", "helm-set-image", "render-manifests", "git-commit", "git-push", "health-check"})
	assert.Equal(t, "helm", ps.Status.Outputs["renderer"])
	dep := e.ReadFile(t, a.repo, "env/test", "deployment-"+fixtures.Workload("test")+".yaml")
	assert.Contains(t, dep, "image: "+v2)
	_, err := e.Git.ReadFile(context.Background(), a.repo, "env/test", "Chart.yaml")
	assert.Error(t, err, "the rendered branch holds no chart")
	a.running(t, "test", v2, "test runs the rendered chart")
	assert.True(t, strings.Contains(e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/"+fixtures.HelmValuesFile), fixtures.V1),
		"the DRY values are not committed to")
}
