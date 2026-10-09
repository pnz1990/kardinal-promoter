//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
//     Bundle's tag in the environment's RenderRun Job (sandboxed: no
//     Kubernetes credentials, read-only root filesystem, limits), one file per
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
	checkSteps(t, ps, []string{"render", "health-check"})
	rr := renderRunOf(t, e, a.ns, b2, "test")
	assertRenderJobSandboxed(t, e, rr)
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
// render Job) with the values file helm-set-image edits, into env/test, and
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
	checkSteps(t, ps, []string{"render", "health-check"})
	assert.Equal(t, "helm", ps.Status.Outputs["renderer"])
	dep := e.ReadFile(t, a.repo, "env/test", "deployment-"+fixtures.Workload("test")+".yaml")
	assert.Contains(t, dep, "image: "+v2)
	_, err := e.Git.ReadFile(context.Background(), a.repo, "env/test", "Chart.yaml")
	assert.Error(t, err, "the rendered branch holds no chart")
	a.running(t, "test", v2, "test runs the rendered chart")
	assert.True(t, strings.Contains(e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/"+fixtures.HelmValuesFile), fixtures.V1),
		"the DRY values are not committed to")
}

// renderRunOf waits for the Succeeded RenderRun of bundle in env.
func renderRunOf(t *testing.T, e *framework.Env, ns, bundle, env string) *v1alpha1.RenderRun {
	t.Helper()
	var got *v1alpha1.RenderRun
	framework.Eventually(t, promoteTimeout, "the RenderRun of "+bundle+" in "+env, func(ctx context.Context) (bool, string) {
		var list v1alpha1.RenderRunList
		if err := e.Client.List(ctx, &list, client.InNamespace(ns),
			client.MatchingLabels{"kardinal.io/bundle": bundle, "kardinal.io/environment": env}); err != nil {
			return false, err.Error()
		}
		if len(list.Items) != 1 {
			return false, fmt.Sprintf("%d RenderRuns", len(list.Items))
		}
		got = &list.Items[0]
		return got.Status.Phase == v1alpha1.RenderRunSucceeded || got.Status.Phase == v1alpha1.RenderRunFailed,
			got.Status.Phase + ": " + got.Status.Message
	})
	return got
}

// assertRenderJobSandboxed checks the render ran out of the controller, in a
// Job with no Kubernetes credentials, a read-only root filesystem, no
// capabilities and limits, as the kardinal-render ServiceAccount, which has
// no token.
func assertRenderJobSandboxed(t *testing.T, e *framework.Env, rr *v1alpha1.RenderRun) {
	t.Helper()
	ctx := context.Background()
	require.Equal(t, v1alpha1.RenderRunSucceeded, rr.Status.Phase, rr.Status.Message)
	require.NotNil(t, rr.Status.Result)
	assert.Len(t, rr.Status.Result.CommitSHA, 40)
	assert.Len(t, rr.Status.Result.MarkerDigest, 64)
	job, err := e.Kube.BatchV1().Jobs(rr.Namespace).Get(ctx, rr.Status.JobName, metav1.GetOptions{})
	require.NoError(t, err)
	pod := job.Spec.Template.Spec
	assert.Equal(t, "kardinal-render", pod.ServiceAccountName)
	require.NotNil(t, pod.AutomountServiceAccountToken)
	assert.False(t, *pod.AutomountServiceAccountToken)
	c := pod.Containers[0]
	assert.True(t, *c.SecurityContext.ReadOnlyRootFilesystem)
	assert.Equal(t, []corev1.Capability{"ALL"}, c.SecurityContext.Capabilities.Drop)
	assert.NotEmpty(t, c.Resources.Limits.Memory().String())
	sa, err := e.Kube.CoreV1().ServiceAccounts(rr.Namespace).Get(ctx, "kardinal-render", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, sa.AutomountServiceAccountToken)
	assert.False(t, *sa.AutomountServiceAccountToken)
	bindings, err := e.Kube.RbacV1().RoleBindings(rr.Namespace).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for _, b := range bindings.Items {
		for _, s := range b.Subjects {
			assert.False(t, s.Kind == "ServiceAccount" && s.Name == "kardinal-render", "RoleBinding %s binds kardinal-render", b.Name)
		}
	}
}

// renderFails promotes v2 of a and waits for the step of env to fail on its
// render; it returns the RenderRun message, and checks that nothing was
// pushed to the rendered branch.
func renderFails(t *testing.T, e *framework.Env, a *app, p *v1alpha1.Pipeline, env string) string {
	t.Helper()
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Failed", promoteTimeout)
	rr := renderRunOf(t, e, a.ns, bundle, env)
	require.Equal(t, v1alpha1.RenderRunFailed, rr.Status.Phase)
	assert.Contains(t, ps.Status.Message, rr.Status.Message)
	commits, err := gitserver.Commits(context.Background(), e.Git, a.repo, "env/"+env, 5)
	if err == nil {
		for _, c := range commits {
			assert.NotContains(t, c.Message, "Kardinal-Bundle: "+bundle, "nothing rendered was pushed")
		}
	}
	return rr.Status.Message
}

// TestCore_RenderedBranchRefusesAttacks runs the attacks QA proved against
// the in-controller renderer, now each in its own render Job, and checks
// that each is refused and nothing is pushed:
//   - SSRF through a configMapGenerator file URL (the cloud metadata
//     endpoint);
//   - a symbolic link in the DRY source to the ServiceAccount token path
//     (the render Pod mounts no token anyway);
//   - a Helm template that doubles a string until it fills the memory;
//   - a diamond of kustomize overlays that multiplies the objects.
//
// Covers REND-SANDBOX-01.
func TestCore_RenderedBranchRefusesAttacks(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	t.Run("ssrf", func(t *testing.T) {
		a := renderedApp(t, e, func(app fixtures.App) map[string][]byte {
			files := fixtures.KustomizeRepo(app)
			files[fixtures.Path("test")+"/kustomization.yaml"] = append(files[fixtures.Path("test")+"/kustomization.yaml"],
				[]byte("configMapGenerator:\n- name: stolen\n  files: [creds=http://169.254.169.254/latest/meta-data/iam/security-credentials/]\n")...)
			return files
		}, "test")
		msg := renderFails(t, e, a, a.renderedPipeline(nil), "test")
		assert.Contains(t, msg, "configMapGenerator[].files[]: remote reference")
	})
	t.Run("symlink to the token", func(t *testing.T) {
		a := renderedApp(t, e, fixtures.KustomizeRepo, "test")
		e.PushTree(t, a.repo, "link the token", func(dir string) {
			require.NoError(t, os.Symlink("/var/run/secrets/kubernetes.io/serviceaccount/token",
				filepath.Join(dir, fixtures.Path("test"), "token.yaml")))
		})
		msg := renderFails(t, e, a, a.renderedPipeline(nil), "test")
		assert.Contains(t, msg, "symbolic link "+fixtures.Path("test")+"/token.yaml in the DRY source is not supported")
	})
	t.Run("doubling template", func(t *testing.T) {
		a := renderedApp(t, e, func(app fixtures.App) map[string][]byte {
			files := fixtures.HelmRepo(app)
			files[fixtures.Path("test")+"/templates/bomb.yaml"] = []byte(`{{ $x := "kardinal" }}{{ range until 64 }}{{ $x = print $x $x }}{{ end }}` +
				"apiVersion: v1\nkind: ConfigMap\nmetadata: {name: bomb}\ndata: {x: {{ $x | quote }}}\n")
			return files
		}, "test")
		p := a.renderedPipeline(nil)
		envSpec(t, p, "test").Update = v1alpha1.UpdateConfig{Strategy: "helm", Helm: &v1alpha1.HelmUpdateConfig{
			ValuesFile: fixtures.HelmValuesFile, ImagePathTemplate: fixtures.HelmImagePath}}
		msg := renderFails(t, e, a, p, "test")
		assert.Contains(t, msg, "larger than the render limit")
	})
	t.Run("diamond", func(t *testing.T) {
		a := renderedApp(t, e, func(app fixtures.App) map[string][]byte {
			files := fixtures.KustomizeRepo(app)
			files["diamond/l0/kustomization.yaml"] = []byte("resources: [cm.yaml]\n")
			files["diamond/l0/cm.yaml"] = []byte("apiVersion: v1\nkind: ConfigMap\nmetadata: {name: leaf}\n")
			for i := 1; i <= 16; i++ {
				files[fmt.Sprintf("diamond/l%d/kustomization.yaml", i)] = []byte(fmt.Sprintf("resources: [../l%d, ../l%d-b]\n", i-1, i-1))
				files[fmt.Sprintf("diamond/l%d-b/kustomization.yaml", i-1)] = []byte(fmt.Sprintf("resources: [../l%d]\nnameSuffix: -b\n", i-1))
			}
			k := strings.Replace(string(files[fixtures.Path("test")+"/kustomization.yaml"]),
				"  - service.yaml\n", "  - service.yaml\n  - ../../diamond/l16\n", 1)
			files[fixtures.Path("test")+"/kustomization.yaml"] = []byte(k)
			return files
		}, "test")
		msg := renderFails(t, e, a, a.renderedPipeline(nil), "test")
		assert.Contains(t, msg, "would produce more than 5000 objects")
	})
}

// TestCore_RenderedBranchOnePipelinePerBranch: a second Pipeline whose
// environment renders to the branch of the first one's (the same repository)
// is Ready=False with reason RenderedBranchConflict, and its promotion fails
// without touching the branch: the render marker names the first Pipeline.
//
// Covers REND-CONFLICT-01.
func TestCore_RenderedBranchOnePipelinePerBranch(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := renderedApp(t, e, fixtures.KustomizeRepo, "test")
	a.apply(t, a.renderedPipeline(nil))
	first := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, first, "test", "Verified", promoteTimeout)
	head := headSHA(t, e, "env/test", a.repo)

	second := a.renderedPipeline(nil)
	second.Name = "intruder"
	second.Spec.Environments[0].Health = v1alpha1.HealthConfig{Type: "resource", Timeout: "1m"}
	require.NoError(t, e.Client.Create(ctx, second))
	t.Cleanup(func() { _ = e.Client.Delete(context.Background(), second) })
	framework.Eventually(t, time.Minute, "the conflict on the newer Pipeline", func(ctx context.Context) (bool, string) {
		var p v1alpha1.Pipeline
		if err := e.Client.Get(ctx, client.ObjectKeyFromObject(second), &p); err != nil {
			return false, err.Error()
		}
		c := meta.FindStatusCondition(p.Status.Conditions, "Ready")
		if c == nil {
			return false, "no Ready"
		}
		return c.Reason == "RenderedBranchConflict", c.Reason + ": " + c.Message
	})
	var mine v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: pipelineName}, &mine))
	assert.Equal(t, metav1.ConditionTrue, meta.FindStatusCondition(mine.Status.Conditions, "Ready").Status, "the older Pipeline keeps it")

	b := e.CreateBundle(t, a.ns, "intruder", "--image", fixtures.Image+":"+fixtures.V3)
	ps := e.WaitStepState(t, a.ns, "intruder", b, "test", "Failed", promoteTimeout)
	assert.Contains(t, ps.Status.Message, "rendered branch env/test is not this environment's: it was rendered for Pipeline "+a.ns+"/"+pipelineName)
	assert.Equal(t, head, headSHA(t, e, "env/test", a.repo), "nothing was pushed")
}
