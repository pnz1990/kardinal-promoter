//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// waveEnvs is how many environments TestHealth_ArgoCDWaveOnSharedBranch runs
// (KARDINAL_E2E_WAVE_ENVS). The default, 30, keeps the core suite's runner
// free for its other tests; 150 is the acceptance case of #1575 (about 11
// minutes on a 32-core host).
func waveEnvs(t *testing.T) int {
	n := 30
	if v := os.Getenv("KARDINAL_E2E_WAVE_ENVS"); v != "" {
		var err error
		n, err = strconv.Atoi(v)
		require.NoError(t, err)
	}
	return n
}

// TestHealth_ArgoCDWaveOnSharedBranch (#1575) runs one auto environment and
// then a wave of all the others on one repository and branch, each with
// health.type argocd on its own Application and a Deployment scaled to zero:
// no Pods, so status.summary.images shows nothing. The wave's pushes move
// the branch under each other, so most Applications sync a later head than
// their step's own commit. Every environment is Verified, those through the
// branch history ("synced revision ... contains ..."), and none times out.
//
// Covers HEALTH-ARGOCD-DESCENDANT-01.
func TestHealth_ArgoCDWaveOnSharedBranch(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	ctx := context.Background()
	n := waveEnvs(t)
	envs := make([]string, n)
	for i := range envs {
		envs[i] = fmt.Sprintf("w%03d", i)
	}
	files := fixtures.KustomizeRepoFor(envs, fixtures.Workload, func(string) string { return ns })
	for _, env := range envs {
		p := fixtures.Path(env) + "/deployment.yaml"
		files[p] = []byte(strings.Replace(string(files[p]), "  replicas: 1\n", "  replicas: 0\n", 1))
	}
	repo := e.Repo(t, ns, files)
	for _, env := range envs {
		e.ArgoApp(t, ns+"-"+env, repo, fixtures.Path(env), ns)
	}
	for _, env := range envs {
		e.WaitArgoApp(t, ns+"-"+env, syncTimeout)
	}
	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "wave", Namespace: ns},
		Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: repo.CloneURL, Branch: repo.Branch,
			SecretRef: &v1alpha1.SecretRef{Name: framework.GitSecretName}}},
	}
	for i, env := range envs {
		es := v1alpha1.EnvironmentSpec{Name: env, Path: fixtures.Path(env), Approval: "auto",
			Update: v1alpha1.UpdateConfig{Strategy: "kustomize"},
			Health: v1alpha1.HealthConfig{Type: "argocd", Timeout: "10m",
				ArgoCD: &v1alpha1.HealthTargetRef{Name: ns + "-" + env, Namespace: framework.ArgoCDNamespace}}}
		if i > 0 {
			es.Wave = 1
		}
		p.Spec.Environments = append(p.Spec.Environments, es)
	}
	require.NoError(t, e.Client.Create(ctx, p))
	a := &app{e: e, ns: ns, repo: repo}
	bundle := a.createBundle(t, v1alpha1.BundleSpec{Pipeline: "wave", Images: podinfoImages(fixtures.V2)})
	start := time.Now()
	framework.Eventually(t, 30*time.Minute, "every wave environment Verified", func(ctx context.Context) (bool, string) {
		var steps v1alpha1.PromotionStepList
		if err := e.Client.List(ctx, &steps, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/bundle": bundle}); err != nil {
			return false, err.Error()
		}
		states := map[string]int{}
		for _, s := range steps.Items {
			states[s.Status.State]++
			if s.Status.State == "Failed" {
				return true, "step " + s.Spec.Environment + " failed: " + s.Status.Message
			}
		}
		return states["Verified"] == n, fmt.Sprintf("%v of %d", states, n)
	})
	var steps v1alpha1.PromotionStepList
	require.NoError(t, e.Client.List(ctx, &steps, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/bundle": bundle}))
	descendant := 0
	for _, s := range steps.Items {
		assert.Equal(t, "Verified", s.Status.State, "%s: %s", s.Spec.Environment, s.Status.Message)
		if strings.Contains(s.Status.Message, "contains") {
			descendant++
		}
	}
	assert.Positive(t, descendant, "some Applications synced a later head that contains the step's commit")
	e.WaitBundlePhase(t, ns, bundle, "Verified", 2*time.Minute)
	t.Logf("%d environments Verified in %s; %d through a later synced revision", n, time.Since(start).Round(time.Second), descendant)
}

// TestHealth_ArgoImagesFallback keeps the images fallback live (#1575 QA): a
// synced revision the branch history says does not contain the promoted
// commit. test's commit is pushed while Argo CD's auto-sync is off; then the
// branch is force-pushed to a rewrite of it, a sibling commit with the same
// kustomization, as a squash-and-force-push would leave it. Argo CD syncs the
// rewrite, whose history (complete: the repo is small) does not contain
// test's commit, so only the Application running the Bundle image passes
// the step, and the message says so.
//
// Covers HEALTH-ARGO-IMAGES-01.
func TestHealth_ArgoImagesFallback(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	b, ok := e.Git.(gitserver.Brancher)
	if !ok {
		t.Fatalf("%s git server cannot create branches", e.Git.Kind())
	}
	require.NoError(t, b.CreateBranch(ctx, a.repo, "rewrite", a.repo.Branch))
	a.apply(t, a.pipeline(nil))
	e.SetArgoAutoSync(t, a.argoApp("test"), false)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStep(t, a.ns, pipelineName, bundle, "test", promoteTimeout, "test's commit",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.State == "HealthChecking" && ps.Status.Outputs["commitSHA"] != "", framework.DescribeStep(ps)
		})
	own := ps.Status.Outputs["commitSHA"]
	path := fixtures.Path("test") + "/kustomization.yaml"
	sibling, err := gitserver.CommitFiles(ctx, e.Git, a.repo, "rewrite", "", "the same change, rewritten",
		map[string][]byte{path: []byte(e.ReadFile(t, a.repo, own, path)), "notes/rewritten.txt": []byte("x\n")})
	require.NoError(t, err)
	scale.ForcePush(t, e, a.repo, sibling)

	e.SetArgoAutoSync(t, a.argoApp("test"), true)
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, fmt.Sprintf("%s (synced revision %s is not %s, but the Application runs the Bundle images)",
		argoVerified, short(sibling), short(own)), ps.Status.Message)
	assert.Equal(t, sibling, e.ArgoField(t, a.argoApp("test"), "status", "sync", "revision"))
	assert.Equal(t, imageV2, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
}
