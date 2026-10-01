//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// fluxSource is the GitRepository every fluxApp creates for its repo.
const fluxSource = "podinfo"

// fluxApp is one test's promotable app on Flux: a namespace, a GitOps repo
// seeded with fixtures.KustomizeRepo, a GitRepository for it and one
// Kustomization per environment, all in the test namespace.
type fluxApp struct {
	e    *framework.Env
	ns   string
	repo gitserver.Repo
	envs []string
}

// newFluxRepo sets up the namespace, the repo (with an overlay per env) and
// the GitRepository fluxSource, which fetches every 5s. It creates no
// Kustomization.
func newFluxRepo(t *testing.T, e *framework.Env, envs ...string) *fluxApp {
	t.Helper()
	ns := e.Namespace(t)
	a := &fluxApp{e: e, ns: ns, envs: envs,
		repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: envs}))}
	e.FluxSource(t, ns, fluxSource, a.repo, 5*time.Second)
	return a
}

// newFluxApp is newFluxRepo plus a synced Kustomization per environment.
func newFluxApp(t *testing.T, e *framework.Env, envs ...string) *fluxApp {
	t.Helper()
	a := newFluxRepo(t, e, envs...)
	for _, env := range envs {
		a.kustomize(t, env, fluxSource, 10*time.Minute)
	}
	for _, env := range envs {
		a.waitSynced(t, env)
	}
	return a
}

// kustomize creates the Kustomization fixtures.Workload(env) that applies
// env's overlay from source into the test namespace and health-checks its
// Deployment.
func (a *fluxApp) kustomize(t *testing.T, env, source string, interval time.Duration) {
	t.Helper()
	a.e.FluxKustomization(t, a.ns, fixtures.Workload(env), map[string]interface{}{
		"interval":        interval.String(),
		"path":            "./" + fixtures.Path(env),
		"prune":           true,
		"targetNamespace": a.ns,
		"sourceRef":       map[string]interface{}{"kind": "GitRepository", "name": source},
		"healthChecks": []interface{}{map[string]interface{}{
			"apiVersion": "apps/v1", "kind": "Deployment", "name": fixtures.Workload(env), "namespace": a.ns,
		}},
		"timeout": "3m",
	})
}

// waitSynced waits until env's Kustomization is Ready and its Deployment runs V1.
func (a *fluxApp) waitSynced(t *testing.T, env string) {
	t.Helper()
	a.e.WaitFluxReady(t, framework.KustomizationGVR, a.ns, fixtures.Workload(env), syncTimeout)
	a.e.WaitDeploymentImage(t, a.ns, fixtures.Workload(env), fixtures.Image+":"+fixtures.V1, syncTimeout)
}

// pipeline is the Pipeline pipelineName over every env of the app.
func (a *fluxApp) pipeline(approval map[string]string) *v1alpha1.Pipeline {
	return a.pipelineOf(pipelineName, approval, a.envs...)
}

// pipelineOf is a Pipeline named name over envs of the app's repo. approval
// maps env to "pr-review"; every other env is auto. Health is flux on env's
// Kustomization.
func (a *fluxApp) pipelineOf(name string, approval map[string]string, envs ...string) *v1alpha1.Pipeline {
	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.ns},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{
				URL:       a.repo.CloneURL,
				Branch:    a.repo.Branch,
				SecretRef: &v1alpha1.SecretRef{Name: framework.GitSecretName},
			},
		},
	}
	for _, env := range envs {
		mode := approval[env]
		if mode == "" {
			mode = "auto"
		}
		p.Spec.Environments = append(p.Spec.Environments, v1alpha1.EnvironmentSpec{
			Name:     env,
			Path:     fixtures.Path(env),
			Approval: mode,
			Update:   v1alpha1.UpdateConfig{Strategy: "kustomize"},
			Health: v1alpha1.HealthConfig{
				Type:    "flux",
				Timeout: "3m",
				Flux:    &v1alpha1.HealthTargetRef{Name: fixtures.Workload(env), Namespace: a.ns},
			},
		})
	}
	return p
}

// apply creates p.
func (a *fluxApp) apply(t *testing.T, p *v1alpha1.Pipeline) {
	t.Helper()
	if err := a.e.Client.Create(context.Background(), p); err != nil {
		t.Fatalf("create Pipeline: %v", err)
	}
}

// kustomization gets env's Kustomization.
func (a *fluxApp) kustomization(t *testing.T, env string) *unstructured.Unstructured {
	t.Helper()
	ks, err := a.e.FluxObject(context.Background(), framework.KustomizationGVR, a.ns, fixtures.Workload(env))
	require.NoError(t, err)
	return ks
}

// fluxRev is how Flux names a commit on main in lastAppliedRevision and
// artifact.revision.
func fluxRev(branch, sha string) string { return branch + "@sha1:" + sha }

// shortSHA is the 12-character commit kardinal prints in health messages.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// stepCommit waits until the step has pushed its commit and returns it.
func stepCommit(t *testing.T, e *framework.Env, ns, pipeline, bundle, env string) string {
	t.Helper()
	var sha string
	framework.Eventually(t, promoteTimeout, fmt.Sprintf("step %s/%s to push its commit", bundle, env), func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, pipeline, bundle, env)
		if err != nil || !ok {
			return false, fmt.Sprintf("no step yet (%v)", err)
		}
		sha = ps.Status.Outputs["commitSHA"]
		return sha != "", fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	return sha
}

// TestFlux_VerifiesAppliedCommit is the Flux journey: a Bundle auto-promotes
// through test and uat, and each step is Verified only once the Kustomization
// applied the commit kardinal pushed to the Forgejo repo. The health message
// names that commit.
//
// Covers HEALTH-FLUX-01, HEALTH-FLUX-04.
func TestFlux_VerifiesAppliedCommit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newFluxApp(t, e, "test", "uat")
	a.apply(t, a.pipeline(nil))

	newImage := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", newImage)
	var commit string
	for _, env := range a.envs {
		ps := e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Verified", promoteTimeout)
		commit = ps.Status.Outputs["commitSHA"]
		require.NotEmpty(t, commit, "%s: an auto promotion pushes a commit", env)
		assert.Contains(t, ps.Status.Message, "health check passed via flux: Ready=True")
		assert.Contains(t, ps.Status.Message, shortSHA(commit), "%s: the pass message names the verified commit", env)
		assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path(env)+"/kustomization.yaml"), "newTag: "+fixtures.V2)
		assert.Equal(t, newImage, e.DeploymentImage(t, a.ns, fixtures.Workload(env)))
	}
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)

	// uat pushed last, so both the source and uat's Kustomization are on its commit.
	src, err := e.FluxObject(context.Background(), framework.GitRepositoryGVR, a.ns, fluxSource)
	require.NoError(t, err)
	assert.Equal(t, fluxRev(a.repo.Branch, commit), framework.FluxArtifactRevision(src),
		"the GitRepository fetched the promoted commit from Forgejo")
	assert.Equal(t, fluxRev(a.repo.Branch, commit), framework.FluxAppliedRevision(a.kustomization(t, "uat")))
}

// TestFlux_UnhealthyKustomizations runs three single-environment Pipelines
// side by side:
//   - gone: no Kustomization. Each check is unhealthy ("not found") and the
//     step fails at its 1m health timeout.
//   - stalled: the release cannot start, so Flux reports Ready=False with
//     stalled resources. The step fails at once, not at its 5m timeout.
//   - behind: the Kustomization is Ready on the previous commit (its source is
//     suspended). The step waits; once the source resumes it is Verified.
//
// Covers HEALTH-FLUX-02, HEALTH-FLUX-08.
func TestFlux_UnhealthyKustomizations(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newFluxRepo(t, e, "gone", "stalled", "behind")
	const frozen = "frozen"
	e.FluxSource(t, a.ns, frozen, a.repo, 5*time.Second)
	a.kustomize(t, "stalled", fluxSource, 10*time.Minute)
	a.kustomize(t, "behind", frozen, 10*time.Minute)
	a.waitSynced(t, "stalled")
	a.waitSynced(t, "behind")
	e.SuspendFlux(t, framework.GitRepositoryGVR, a.ns, frozen, true)

	gone := a.pipelineOf("gone", nil, "gone")
	gone.Spec.Environments[0].Health.Timeout = "1m"
	stalled := a.pipelineOf("stalled", nil, "stalled")
	stalled.Spec.Environments[0].Health.Timeout = "5m"
	behind := a.pipelineOf("behind", nil, "behind")
	behind.Spec.Environments[0].Health.Timeout = "5m"
	for _, p := range []*v1alpha1.Pipeline{gone, stalled, behind} {
		a.apply(t, p)
	}
	goneBundle := e.CreateBundle(t, a.ns, "gone", "--image", fixtures.Image+":"+fixtures.V2)
	stalledBundle := e.CreateBundle(t, a.ns, "stalled", "--image", fixtures.Image+":"+fixtures.BrokenTag)
	newImage := fixtures.Image + ":" + fixtures.V2
	behindBundle := e.CreateBundle(t, a.ns, "behind", "--image", newImage)

	// gone: unhealthy on every check, then its 1m timeout fails it. Checked
	// first: the timeout runs from its first health check.
	framework.Eventually(t, time.Minute, "gone to report the missing Kustomization", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, "gone", goneBundle, "gone")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		want := fmt.Sprintf("unhealthy via flux: Kustomization %s/%s not found", a.ns, fixtures.Workload("gone"))
		return ps.Status.Message == want, fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	ps := e.WaitStepState(t, a.ns, "gone", goneBundle, "gone", "Failed", 2*time.Minute)
	assert.Contains(t, ps.Status.Message, "health check timeout after 1m0s; last result: unhealthy via flux: Kustomization")
	e.WaitBundlePhase(t, a.ns, goneBundle, "Failed", time.Minute)

	// behind: Ready=True on the old commit is not the promoted commit.
	commit := stepCommit(t, e, a.ns, "behind", behindBundle, "behind")
	framework.Eventually(t, time.Minute, "behind to wait for its commit", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, "behind", behindBundle, "behind")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return ps.Status.State == "HealthChecking" && strings.Contains(ps.Status.Message, "waiting for "+shortSHA(commit)),
			fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	ps, _, err := e.Step(context.Background(), a.ns, "behind", behindBundle, "behind")
	require.NoError(t, err)
	assert.Contains(t, ps.Status.Message, "waiting for flux: Ready=True")
	assert.Contains(t, ps.Status.Message, "lastAppliedRevision=")

	// stalled: Flux gives up on the stalled Deployment after its 60s progress
	// deadline; the step fails well before its 5m health timeout.
	ps = e.WaitStepState(t, a.ns, "stalled", stalledBundle, "stalled", "Failed", 3*time.Minute)
	assert.Contains(t, ps.Status.Message, "health alarm via flux (onHealthFailure=none)")
	assert.Contains(t, ps.Status.Message, "stalled resources")
	assert.NotContains(t, ps.Status.Message, "health check timeout", "a stalled Kustomization fails before the timeout")
	e.WaitBundlePhase(t, a.ns, stalledBundle, "Failed", time.Minute)

	// behind kept waiting on the old release the whole time.
	ps = e.WaitStepState(t, a.ns, "behind", behindBundle, "behind", "HealthChecking", time.Second)
	assert.Contains(t, ps.Status.Message, "waiting for "+shortSHA(commit))
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("behind")))
	e.SuspendFlux(t, framework.GitRepositoryGVR, a.ns, frozen, false)
	e.WaitStepState(t, a.ns, "behind", behindBundle, "behind", "Verified", promoteTimeout)
	assert.Equal(t, newImage, e.DeploymentImage(t, a.ns, fixtures.Workload("behind")))
	assert.Equal(t, fluxRev(a.repo.Branch, commit), framework.FluxAppliedRevision(a.kustomization(t, "behind")))
}

// TestFlux_PRReviewWaitsForMergeCommit checks that a pr-review environment
// on flux health waits for Flux to apply the PR's merge commit: a
// Kustomization still Ready on the previous commit after the merge does not
// verify the step.
//
// Covers HEALTH-FLUX-03.
func TestFlux_PRReviewWaitsForMergeCommit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newFluxApp(t, e, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	newImage := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", newImage)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool { return pr.State == "open" })

	// Hold Flux on the pre-merge commit, then merge.
	e.SuspendFlux(t, framework.GitRepositoryGVR, a.ns, fluxSource, true)
	require.NoError(t, e.Git.MergePR(context.Background(), a.repo, pr.Number))

	var merge string
	framework.Eventually(t, 2*time.Minute, "prod to wait for the merge commit", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		merge = ps.Status.Outputs["mergeCommitSHA"]
		return ps.Status.State == "HealthChecking" && merge != "" &&
				strings.Contains(ps.Status.Message, "waiting for "+shortSHA(merge)),
			fmt.Sprintf("state=%q merge=%q message=%q", ps.Status.State, merge, ps.Status.Message)
	})
	framework.Consistently(t, 20*time.Second, "prod waits while Flux is on the previous commit", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return ps.Status.State == "HealthChecking" && strings.Contains(ps.Status.Message, "waiting for flux: Ready=True"),
			fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))

	e.SuspendFlux(t, framework.GitRepositoryGVR, a.ns, fluxSource, false)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, newImage, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	assert.Equal(t, fluxRev(a.repo.Branch, merge), framework.FluxAppliedRevision(a.kustomization(t, "prod")))
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// TestFlux_SuspendedKustomization checks that a promotion into a suspended
// Kustomization says so while it waits, and goes on once it is resumed.
//
// Covers HEALTH-FLUX-05.
func TestFlux_SuspendedKustomization(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newFluxApp(t, e, "test")
	e.SuspendFlux(t, framework.KustomizationGVR, a.ns, fixtures.Workload("test"), true)
	a.apply(t, a.pipeline(nil))

	newImage := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", newImage)
	framework.Eventually(t, time.Minute, "the step to say the Kustomization is suspended", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return ps.Status.State == "HealthChecking" && strings.Contains(ps.Status.Message, "is suspended"),
			fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	ps, _, err := e.Step(context.Background(), a.ns, pipelineName, bundle, "test")
	require.NoError(t, err)
	assert.Contains(t, ps.Status.Message, fmt.Sprintf("waiting for flux: Kustomization %s/%s is suspended", a.ns, fixtures.Workload("test")))
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))

	e.SuspendFlux(t, framework.KustomizationGVR, a.ns, fixtures.Workload("test"), false)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, newImage, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
}

// TestFlux_SiblingEnvsShareBranch promotes two parallel environments of one
// Pipeline. Both push to the Pipeline branch, so Flux applies the later
// commit to both Kustomizations; the environment that pushed first must
// still be Verified, because its Deployment runs the Bundle image.
//
// Covers HEALTH-FLUX-06.
func TestFlux_SiblingEnvsShareBranch(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newFluxApp(t, e, "prod-us", "prod-eu")
	p := a.pipeline(nil)
	for i := range p.Spec.Environments {
		p.Spec.Environments[i].Wave = 1
	}
	// Flux fetches only after both environments pushed.
	e.SuspendFlux(t, framework.GitRepositoryGVR, a.ns, fluxSource, true)
	a.apply(t, p)

	newImage := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", newImage)
	commits := map[string]string{}
	for _, env := range a.envs {
		commits[env] = stepCommit(t, e, a.ns, pipelineName, bundle, env)
	}
	require.NotEqual(t, commits["prod-us"], commits["prod-eu"], "each environment pushes its own commit")
	e.SuspendFlux(t, framework.GitRepositoryGVR, a.ns, fluxSource, false)

	noted := 0
	for _, env := range a.envs {
		ps := e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Verified", promoteTimeout)
		assert.Equal(t, newImage, e.DeploymentImage(t, a.ns, fixtures.Workload(env)))
		if strings.Contains(ps.Status.Message, "but the Kustomization's Deployments run the Bundle images") {
			noted++
			assert.Contains(t, ps.Status.Message, shortSHA(commits[env]), "the note names the commit the step pushed")
		}
	}
	assert.Equal(t, 1, noted, "the environment whose commit Flux skipped says why it is Verified anyway")
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// TestFlux_BakeSurvivesFluxReconcile bakes an environment with
// fail-on-alarm while Flux reconciles its Kustomization again and again
// (as `flux reconcile` in a loop, a busy webhook receiver or a short interval
// does). Flux marks the Kustomization Ready=Unknown during each reconcile,
// so it is Ready=Unknown at most health checks; while the Deployment stays
// healthy the bake window must neither alarm nor stop and start over.
//
// Covers HEALTH-FLUX-07.
func TestFlux_BakeSurvivesFluxReconcile(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newFluxApp(t, e, "prod")
	p := a.pipeline(nil)
	p.Spec.Environments[0].Bake = &v1alpha1.BakeConfig{Minutes: 1, Policy: "fail-on-alarm"}
	a.apply(t, p)

	newImage := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", newImage)
	var started metav1.Time
	framework.Eventually(t, promoteTimeout, "the bake to start", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		if ps.Status.State == "Failed" {
			t.Fatalf("step failed before the bake: %s", ps.Status.Message)
		}
		if ps.Status.BakeStartedAt != nil {
			started = *ps.Status.BakeStartedAt
		}
		return ps.Status.BakeStartedAt != nil, fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})

	stop := e.KeepFluxReconciling(t, framework.KustomizationGVR, a.ns, fixtures.Workload("prod"))
	var final *v1alpha1.PromotionStep
	framework.Eventually(t, 3*time.Minute, "the bake to complete while Flux reconciles", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		switch {
		case ps.Status.State == "Failed":
			t.Fatalf("the bake failed during a Flux reconcile: %s", ps.Status.Message)
		case ps.Status.State != "Verified" && (ps.Status.BakeStartedAt == nil || !ps.Status.BakeStartedAt.Equal(&started)):
			t.Fatalf("the bake window stopped during a Flux reconcile (started %s, now %v): %s",
				started.Format(time.RFC3339), ps.Status.BakeStartedAt, ps.Status.Message)
		}
		final = ps
		return ps.Status.State == "Verified", fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	handled := stop()
	t.Logf("Flux handled %d reconcile requests during the bake", handled)
	assert.Greater(t, handled, 10, "Flux reconciled the Kustomization again and again during the bake")
	assert.Equal(t, "bake complete: 1m contiguous healthy via flux (resets=0)", final.Status.Message)
	assert.Equal(t, newImage, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
}

// TestFlux_ExampleFluxDemo runs examples/flux-demo as shipped, with the
// repo, names and image pointed at the e2e fixtures and the bakes shortened
// to 1m. The Kustomizations keep the example's 1m interval and the
// <pipeline>-<env> names in flux-system that the flux adapter derives.
// A good Bundle bakes through uat and prod (after its PR merges); a Bundle
// whose uat Deployment turns unready during the bake fails there
// (fail-on-alarm), and prod keeps the good release.
//
// Covers EX-FLUX-01.
func TestFlux_ExampleFluxDemo(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	envs := []string{"test", "uat", "prod"}
	repo := e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: envs}))
	name := "fd-" + ns[len(ns)-8:]

	// The example's GitRepository and Kustomizations, retargeted.
	raw, err := os.ReadFile("../../../examples/flux-demo/flux-kustomizations.yaml")
	require.NoError(t, err)
	for _, doc := range strings.Split(string(raw), "\n---\n") {
		var obj map[string]interface{}
		require.NoError(t, yaml.Unmarshal([]byte(doc), &obj))
		u := unstructured.Unstructured{Object: obj}
		require.Equal(t, framework.FluxNamespace, u.GetNamespace(), "the example puts %s in flux-system", u.GetName())
		spec := obj["spec"].(map[string]interface{})
		switch u.GetKind() {
		case "GitRepository":
			spec["url"] = repo.CloneURL
			spec["ref"] = map[string]interface{}{"branch": repo.Branch}
			spec["interval"] = "10s" // the example's 1m fetch lag would add minutes per environment
			e.FluxGitRepository(t, framework.FluxNamespace, name, spec)
		case "Kustomization":
			env := strings.TrimPrefix(u.GetName(), "flux-demo-")
			require.Contains(t, envs, env)
			spec["targetNamespace"] = ns
			spec["sourceRef"] = map[string]interface{}{"kind": "GitRepository", "name": name}
			spec["healthChecks"] = []interface{}{map[string]interface{}{
				"apiVersion": "apps/v1", "kind": "Deployment", "name": fixtures.Workload(env), "namespace": ns,
			}}
			e.FluxKustomization(t, framework.FluxNamespace, name+"-"+env, spec)
		default:
			t.Fatalf("unexpected %s in the example", u.GetKind())
		}
	}
	for _, env := range envs {
		e.WaitFluxReady(t, framework.KustomizationGVR, framework.FluxNamespace, name+"-"+env, syncTimeout)
		e.WaitDeploymentImage(t, ns, fixtures.Workload(env), fixtures.Image+":"+fixtures.V1, syncTimeout)
	}

	// The example's Pipeline, retargeted. Strict: a field the API dropped fails here.
	raw, err = os.ReadFile("../../../examples/flux-demo/pipeline.yaml")
	require.NoError(t, err)
	var p v1alpha1.Pipeline
	require.NoError(t, yaml.UnmarshalStrict(raw, &p))
	p.Name, p.Namespace = name, ns
	p.Spec.Git.URL, p.Spec.Git.Branch = repo.CloneURL, repo.Branch
	p.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: framework.GitSecretName}
	for i := range p.Spec.Environments {
		env := &p.Spec.Environments[i]
		require.Equal(t, "flux", env.Health.Type)
		require.Nil(t, env.Health.Flux, "the example relies on the derived Kustomization name")
		if env.Bake != nil {
			require.Equal(t, "fail-on-alarm", env.Bake.Policy)
			env.Bake.Minutes = 1
		}
	}
	require.NoError(t, e.Client.Create(context.Background(), &p))

	// A good release bakes through uat, and through prod once its PR merges.
	good := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, ns, name, "--image", good)
	ps := e.WaitStepState(t, ns, name, bundle, "test", "Verified", promoteTimeout)
	assert.Contains(t, ps.Status.Message, "health check passed via flux")
	ps = e.WaitStepState(t, ns, name, bundle, "uat", "Verified", promoteTimeout)
	assert.Equal(t, "bake complete: 1m contiguous healthy via flux (resets=0)", ps.Status.Message)
	e.WaitStepState(t, ns, name, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	require.NoError(t, e.Git.MergePR(context.Background(), repo, pr.Number))
	ps = e.WaitStepState(t, ns, name, bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, "bake complete: 1m contiguous healthy via flux (resets=0)", ps.Status.Message)
	for _, env := range envs {
		assert.Equal(t, good, e.DeploymentImage(t, ns, fixtures.Workload(env)))
	}
	e.WaitBundlePhase(t, ns, bundle, "Verified", time.Minute)

	// A release that turns unready during the uat bake fails there.
	bad := e.CreateBundle(t, ns, name, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitStepState(t, ns, name, bad, "test", "Verified", promoteTimeout)
	framework.Eventually(t, promoteTimeout, "the uat bake to start", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, ns, name, bad, "uat")
		if err != nil || ps == nil {
			return false, "no uat step yet"
		}
		return ps.Status.BakeStartedAt != nil, fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	e.FailReadiness(t, ns, fixtures.Workload("uat"))
	framework.Eventually(t, time.Minute, "the uat Deployment to lose its available replica", func(ctx context.Context) (bool, string) {
		d, err := e.Kube.AppsV1().Deployments(ns).Get(ctx, fixtures.Workload("uat"), metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return d.Status.AvailableReplicas == 0, fmt.Sprintf("available=%d", d.Status.AvailableReplicas)
	})
	// Flux re-checks health at its next reconcile; ask for it now.
	e.ReconcileFlux(t, framework.KustomizationGVR, framework.FluxNamespace, name+"-uat")
	ps = e.WaitStepState(t, ns, name, bad, "uat", "Failed", 2*time.Minute)
	assert.Contains(t, ps.Status.Message, "health alarm via flux (onHealthFailure=none)")
	e.WaitBundlePhase(t, ns, bad, "Failed", time.Minute)

	framework.Consistently(t, 15*time.Second, "prod to keep the good release", func(ctx context.Context) (bool, string) {
		prs, err := e.Git.PullRequests(ctx, repo)
		if err != nil {
			return false, err.Error()
		}
		_, started, err := e.Step(ctx, ns, name, bad, "prod")
		if err != nil {
			return false, err.Error()
		}
		return len(prs) == 1 && !started, fmt.Sprintf("%d PRs, prod step exists: %t", len(prs), started)
	})
	assert.Equal(t, good, e.DeploymentImage(t, ns, fixtures.Workload("prod")))
	assert.Contains(t, e.ReadFile(t, repo, repo.Branch, fixtures.Path("prod")+"/kustomization.yaml"), "newTag: "+fixtures.V2)
}
