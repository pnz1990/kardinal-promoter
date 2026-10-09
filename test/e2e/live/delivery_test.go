//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The delivery suite's adapters and how a test selects them.
const (
	rollouts = "argoRollouts"
	flagger  = "flagger"
)

// Delivery timings. The Rollout fixture's progress deadline is 30s and the
// Canary's 60s; Flagger analyzes every 10s and kardinal checks health every
// 10s, so deliveryHold spans two checks of each.
const (
	deliveryTimeout = 5 * time.Minute
	deliveryHold    = 25 * time.Second
)

// newRolloutApp is newArgoApp over fixtures.RolloutRepo: each environment's
// Rollout, with a canary pause of pause, is Healthy on V1.
func newRolloutApp(t *testing.T, e *framework.Env, pause string, envs ...string) *app {
	t.Helper()
	ns := e.Namespace(t)
	a := &app{e: e, ns: ns, envs: envs,
		repo: e.Repo(t, ns, fixtures.RolloutRepo(fixtures.App{Namespace: ns, Envs: envs}, pause))}
	for _, env := range envs {
		e.ArgoApp(t, a.argoApp(env), a.repo, fixtures.Path(env), ns)
	}
	for _, env := range envs {
		e.WaitArgoApp(t, a.argoApp(env), syncTimeout)
		e.WaitRolloutHealthy(t, ns, fixtures.Workload(env), fixtures.Image+":"+fixtures.V1, syncTimeout)
	}
	return a
}

// newCanaryApp is newArgoApp over fixtures.CanaryRepo: Flagger initialized
// each environment's Canary, and its primary Deployment runs V1.
func newCanaryApp(t *testing.T, e *framework.Env, envs ...string) *app {
	t.Helper()
	ns := e.Namespace(t)
	a := &app{e: e, ns: ns, envs: envs,
		repo: e.Repo(t, ns, fixtures.CanaryRepo(fixtures.App{Namespace: ns, Envs: envs}))}
	for _, env := range envs {
		e.ArgoApp(t, a.argoApp(env), a.repo, fixtures.Path(env), ns)
	}
	for _, env := range envs {
		e.WaitCanaryPhase(t, ns, fixtures.Workload(env), "Initialized", syncTimeout)
		e.WaitDeploymentImage(t, ns, fixtures.PrimaryWorkload(env), fixtures.Image+":"+fixtures.V1, syncTimeout)
	}
	return a
}

// deliveryPipeline is a.pipeline (every environment auto) checking each
// environment's Rollout or Canary with adapter. With delegate it is selected
// by delivery.delegate and health.type stays argocd, which the delegate
// overrides; without, by health.type. tune, when set, edits each environment.
func (a *app) deliveryPipeline(adapter string, delegate bool, tune func(*v1alpha1.EnvironmentSpec)) *v1alpha1.Pipeline {
	p := a.pipeline(nil)
	for i := range p.Spec.Environments {
		env := &p.Spec.Environments[i]
		target(env, a.ns, adapter)
		if delegate {
			env.Delivery.Delegate = adapter
		} else {
			env.Health.Type = adapter
			env.Health.ArgoCD = nil
		}
		if tune != nil {
			tune(env)
		}
	}
	return p
}

// target points env's adapter at the test's Rollout or Canary, which the
// fixtures name after the environment.
func target(env *v1alpha1.EnvironmentSpec, ns, adapter string) {
	ref := &v1alpha1.HealthTargetRef{Name: fixtures.Workload(env.Name), Namespace: ns}
	switch adapter {
	case rollouts:
		env.Health.ArgoRollouts = ref
	case flagger:
		env.Health.Flagger = ref
	}
}

// examplePipeline is the environment env of the example Pipeline at path
// (relative to the repo root) over the app's repo: git, the path and the
// health target are the test's, everything else is the example's. tune, when
// set, edits the environment.
func (a *app) examplePipeline(t *testing.T, path, env string, tune func(*v1alpha1.EnvironmentSpec)) *v1alpha1.Pipeline {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", path))
	require.NoError(t, err)
	var example v1alpha1.Pipeline
	require.NoError(t, yaml.UnmarshalStrict(raw, &example), path)
	p := a.pipeline(nil)
	p.Spec.Environments = nil
	for _, spec := range example.Spec.Environments {
		if spec.Name != env {
			continue
		}
		spec.Path = fixtures.Path(env)
		target(&spec, a.ns, spec.Health.Type)
		if tune != nil {
			tune(&spec)
		}
		p.Spec.Environments = append(p.Spec.Environments, spec)
	}
	require.Len(t, p.Spec.Environments, 1, "%s has environment %s", path, env)
	return p
}

// notUpdated is the start of the message of a step whose Rollout or Canary
// does not run the Bundle images yet.
func notUpdated(ns, adapter, env string) string {
	if adapter == flagger {
		return "waiting for flagger: Canary " + ns + "/" + fixtures.Workload(env) + ": target Deployment " +
			ns + "/" + fixtures.Workload(env) + " not updated yet"
	}
	return "waiting for argoRollouts: Rollout " + ns + "/" + fixtures.Workload(env) + " not updated yet"
}

// holdOldRevision stops Argo CD from applying env's new commits, as a slow
// GitOps sync would, and creates a Bundle for image. It returns once the
// promotion is committed and the step checks a workload that still runs the
// previous release, and fails the test if the step then decides anything
// during deliveryHold. Resume with SetArgoAutoSync(true).
func (a *app) holdOldRevision(t *testing.T, adapter, env, image, what string) string {
	t.Helper()
	a.e.SetArgoAutoSync(t, a.argoApp(env), false)
	bundle := a.e.CreateBundle(t, a.ns, pipelineName, "--image", image)
	a.e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, env, "HealthChecking", promoteTimeout,
		notUpdated(a.ns, adapter, env))
	tag := image[strings.LastIndex(image, ":")+1:]
	require.Contains(t, a.e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path(env)+"/kustomization.yaml"),
		"newTag: "+tag, "the promotion is committed")
	a.e.HoldStep(t, deliveryHold, a.ns, pipelineName, bundle, env, what, func(ps *v1alpha1.PromotionStep) bool {
		return ps.Status.State == "HealthChecking" && ps.Status.ConsecutiveHealthFailures == 0 &&
			strings.Contains(ps.Status.Message, notUpdated(a.ns, adapter, env))
	})
	return bundle
}

// TestRollouts_VerifiedOnBundleRevision checks that delivery.delegate:
// argoRollouts verifies a release only once its Rollout finished rolling it
// out. While Argo CD has not applied the commit, the Rollout is Healthy on the
// previous image, which must not verify the Bundle (bug 1); a canary paused
// at a step waits without counting failures; promoting it verifies the step.
// health.type is argocd, which the delegate overrides.
//
// Covers HEALTH-ROLL-01, HEALTH-ROLL-03, HEALTH-ROLL-04, DELIVERY-01.
func TestRollouts_VerifiedOnBundleRevision(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newRolloutApp(t, e, fixtures.IndefinitePause, "prod")
	a.apply(t, a.deliveryPipeline(rollouts, true, nil))
	rollout, v2 := fixtures.Workload("prod"), fixtures.Image+":"+fixtures.V2

	bundle := a.holdOldRevision(t, rollouts, "prod", v2, "a Healthy Rollout on V1 does not verify V2")

	e.SetArgoAutoSync(t, a.argoApp("prod"), true)
	e.WaitRollout(t, a.ns, rollout, syncTimeout, "paused at the canary step on V2", func(r framework.Rollout) bool {
		return r.Image == v2 && r.Phase == "Paused"
	})
	e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, "prod", "HealthChecking", time.Minute,
		"waiting for argoRollouts: Rollout phase: Paused")
	e.HoldStep(t, deliveryHold, a.ns, pipelineName, bundle, "prod", "a paused canary waits", func(ps *v1alpha1.PromotionStep) bool {
		return ps.Status.State == "HealthChecking" && ps.Status.ConsecutiveHealthFailures == 0
	})

	e.PromoteRollout(t, a.ns, rollout)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", deliveryTimeout)
	assert.Contains(t, ps.Status.Message, "health check passed via argoRollouts: Rollout phase: Healthy")
	r, err := e.GetRollout(context.Background(), a.ns, rollout)
	require.NoError(t, err)
	assert.True(t, r.Image == v2 && r.Phase == "Healthy" && r.Done, "Verified means the Rollout finished V2: %s", r)
	assert.Equal(t, v2, e.StableImage(t, a.ns, rollout))
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// TestRollouts_ConfigBundleWaitsForRollout checks health.type: argoRollouts
// for a config Bundle, which has no images to compare (#1422). Argo CD's
// automated sync is off, so the Rollout is still Healthy on the previous
// spec, its generation observed, when the step's health check runs: that
// phase used to verify the step. It now counts only when Argo Rollouts
// reported the Rollout Healthy after the change reached git, so the step
// waits with no failure, and is Verified once Argo CD applies the config and
// the Rollout rolls it out.
//
// Covers HEALTH-ROLL-06.
func TestRollouts_ConfigBundleWaitsForRollout(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newRolloutApp(t, e, "10s", "prod")
	a.apply(t, a.deliveryPipeline(rollouts, false, nil))
	rollout := fixtures.Workload("prod")
	file := fixtures.Path("prod") + "/rollout.yaml"

	e.SetArgoAutoSync(t, a.argoApp("prod"), false)
	manifest := string(fixtures.RolloutRepo(fixtures.App{Namespace: a.ns, Envs: []string{"prod"}}, "10s")[file])
	cfg := e.Repo(t, a.ns+"-config", map[string][]byte{file: []byte(withConfigChange(manifest))})
	commits, err := gitserver.Commits(context.Background(), e.Git, cfg, cfg.Branch, 1)
	require.NoError(t, err)
	require.NotEmpty(t, commits)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", commits[0].SHA,
		"--config-repo", cfg.CloneURL)

	stale := fmt.Sprintf("waiting for argoRollouts: Rollout %s/%s: Rollout phase: Healthy is for an earlier release: "+
		"its Healthy condition's lastTransitionTime ", a.ns, rollout)
	e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, "prod", "HealthChecking", promoteTimeout,
		stale, " is before the promoted change reached git (", "; waiting for Argo Rollouts to roll out the change")
	require.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, file), configValue, "the config change is in git")
	e.HoldStep(t, deliveryHold, a.ns, pipelineName, bundle, "prod", "a Healthy Rollout on the previous spec does not verify the config",
		func(ps *v1alpha1.PromotionStep) bool {
			return ps.Status.State == "HealthChecking" && ps.Status.ConsecutiveHealthFailures == 0 &&
				strings.HasPrefix(ps.Status.Message, stale)
		})

	e.SetArgoAutoSync(t, a.argoApp("prod"), true)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", deliveryTimeout)
	assert.Contains(t, ps.Status.Message, "health check passed via argoRollouts: Rollout phase: Healthy")
	u, err := e.Dynamic.Resource(framework.RolloutGVR).Namespace(a.ns).Get(context.Background(), rollout, metav1.GetOptions{})
	require.NoError(t, err)
	containers, _, _ := unstructured.NestedSlice(u.Object, "spec", "template", "spec", "containers")
	require.NotEmpty(t, containers)
	assert.Contains(t, fmt.Sprint(containers[0]), configValue, "Verified means the Rollout runs the config")
	r, err := e.GetRollout(context.Background(), a.ns, rollout)
	require.NoError(t, err)
	assert.True(t, r.Phase == "Healthy" && r.Done, "Verified means the Rollout finished: %s", r)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// TestRollouts_BrokenReleaseNeverVerified checks health.type: argoRollouts on
// a release whose pods never start. The Rollout is Healthy on V1 until Argo CD
// applies the commit, which must not verify the Bundle (bug 1); the canary
// then waits (Progressing) without counting failures, Argo Rollouts aborts it
// at its progress deadline and the step counts the Degraded phase as
// unhealthy, and fails at health.timeout. The stable pods stay on V1.
//
// Covers HEALTH-ROLL-02, HEALTH-ROLL-04.
func TestRollouts_BrokenReleaseNeverVerified(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newRolloutApp(t, e, "10s", "prod")
	a.apply(t, a.deliveryPipeline(rollouts, false, func(env *v1alpha1.EnvironmentSpec) { env.Health.Timeout = "2m" }))
	rollout, broken := fixtures.Workload("prod"), fixtures.Image+":"+fixtures.BrokenTag

	bundle := a.holdOldRevision(t, rollouts, "prod", broken, "a Healthy Rollout on V1 does not verify the broken release")

	e.SetArgoAutoSync(t, a.argoApp("prod"), true)
	ps := e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, "prod", "HealthChecking", time.Minute,
		"waiting for argoRollouts: Rollout phase: Progressing")
	assert.Zero(t, ps.Status.ConsecutiveHealthFailures, "a canary in progress is not a health failure")
	e.WaitRollout(t, a.ns, rollout, 2*time.Minute, "aborted", func(r framework.Rollout) bool {
		return r.Aborted && r.Phase == "Degraded"
	})
	ps = e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, "prod", "HealthChecking", time.Minute,
		"unhealthy via argoRollouts: Rollout phase: Degraded")
	assert.Positive(t, ps.Status.ConsecutiveHealthFailures, "Degraded counts as a health failure")

	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", 3*time.Minute)
	assert.Contains(t, ps.Status.Message, "health alarm via argoRollouts (onHealthFailure=none): health check timeout after 2m0s; "+
		"last result: unhealthy via argoRollouts: Rollout phase: Degraded")
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.StableImage(t, a.ns, rollout), "the aborted Rollout serves V1")
}

// TestRollouts_DegradedRolloutRecovers checks that the Degraded phase of an
// aborted release does not count against the next one (bug 9): while Argo CD
// has not applied the fix, the Rollout is still Degraded from the broken
// Bundle, and the fix's step must wait without counting health failures. It
// is Verified with none once the fix rolls out.
//
// Covers HEALTH-ROLL-05.
func TestRollouts_DegradedRolloutRecovers(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newRolloutApp(t, e, "10s", "prod")
	a.apply(t, a.deliveryPipeline(rollouts, false, nil))
	rollout, v2 := fixtures.Workload("prod"), fixtures.Image+":"+fixtures.V2

	first := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
	e.WaitStepMessageAll(t, a.ns, pipelineName, first, "prod", "HealthChecking", deliveryTimeout,
		"unhealthy via argoRollouts: Rollout phase: Degraded")

	fix := a.holdOldRevision(t, rollouts, "prod", v2, "a Degraded Rollout of the broken release is not V2's health")
	e.WaitBundlePhase(t, a.ns, first, "Superseded", time.Minute)

	e.SetArgoAutoSync(t, a.argoApp("prod"), true)
	ps := e.WaitStepState(t, a.ns, pipelineName, fix, "prod", "Verified", deliveryTimeout)
	assert.Contains(t, ps.Status.Message, "health check passed via argoRollouts: Rollout phase: Healthy")
	assert.Zero(t, ps.Status.ConsecutiveHealthFailures)
	e.WaitRolloutHealthy(t, a.ns, rollout, v2, time.Second)
	assert.Equal(t, v2, e.StableImage(t, a.ns, rollout))
}

// TestRollouts_BakeWaitsThroughPause checks bake.policy: fail-on-alarm with a
// Rollout paused by hand during the bake window (bug 4). A paused Rollout is
// waiting, not failing: the window stops but the step does not fail, and once
// the Rollout resumes a full window of healthy checks verifies the step.
//
// Covers BAKE-04, HEALTH-ROLL-03.
func TestRollouts_BakeWaitsThroughPause(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newRolloutApp(t, e, "10s", "prod")
	a.apply(t, a.deliveryPipeline(rollouts, false, func(env *v1alpha1.EnvironmentSpec) {
		env.Bake = &v1alpha1.BakeConfig{Minutes: 1, Policy: "fail-on-alarm"}
	}))
	rollout := fixtures.Workload("prod")

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, "prod", "HealthChecking", deliveryTimeout,
		"bake: 0m/1m contiguous healthy via argoRollouts")

	e.PauseRollout(t, a.ns, rollout, true)
	e.WaitRollout(t, a.ns, rollout, time.Minute, "paused", func(r framework.Rollout) bool { return r.Phase == "Paused" })
	e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, "prod", "HealthChecking", time.Minute,
		"bake: window stopped, waiting for argoRollouts: Rollout phase: Paused")
	e.HoldStep(t, deliveryHold, a.ns, pipelineName, bundle, "prod", "a paused Rollout stops the bake without failing it",
		func(ps *v1alpha1.PromotionStep) bool {
			return ps.Status.State == "HealthChecking" && ps.Status.BakeStartedAt == nil &&
				ps.Status.ConsecutiveHealthFailures == 0
		})

	e.PauseRollout(t, a.ns, rollout, false)
	resumed := time.Now()
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", 3*time.Minute)
	assert.Equal(t, "bake complete: 1m contiguous healthy via argoRollouts (resets=0)", ps.Status.Message)
	assert.GreaterOrEqual(t, time.Since(resumed), time.Minute, "the window restarted when the Rollout resumed")
}

// TestRollouts_BakeResetOnAlarmEnds checks bake.policy: reset-on-alarm on a
// Rollout that breaks during the bake window and stays broken (bug 4). A
// change outside kardinal (here a readiness probe that never passes) makes
// Argo Rollouts abort to Degraded; the window stops, and the step fails at
// health.timeout instead of waiting forever for a healthy check.
//
// Covers BAKE-05.
func TestRollouts_BakeResetOnAlarmEnds(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newRolloutApp(t, e, "10s", "prod")
	a.apply(t, a.deliveryPipeline(rollouts, false, func(env *v1alpha1.EnvironmentSpec) {
		env.Health.Timeout = "2m"
		env.Bake = &v1alpha1.BakeConfig{Minutes: 5, Policy: "reset-on-alarm"}
	}))
	rollout := fixtures.Workload("prod")

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, "prod", "HealthChecking", deliveryTimeout,
		"bake: 0m/5m contiguous healthy via argoRollouts")

	e.SetArgoAutoSync(t, a.argoApp("prod"), false)
	breakReadiness(t, e, a.ns, rollout)
	e.WaitRollout(t, a.ns, rollout, 2*time.Minute, "aborted", func(r framework.Rollout) bool {
		return r.Aborted && r.Phase == "Degraded"
	})
	e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, "prod", "HealthChecking", time.Minute,
		"(unhealthy via argoRollouts): Rollout phase: Degraded")

	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", 3*time.Minute)
	assert.Contains(t, ps.Status.Message, "health alarm via argoRollouts (onHealthFailure=none): health check timeout after 2m0s; last result: bake: ")
	assert.Contains(t, ps.Status.Message, "Rollout phase: Degraded")
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
}

// breakReadiness points the Rollout's readiness probe at a path podinfo does
// not serve: a new revision with the same image whose pods never get ready.
func breakReadiness(t *testing.T, e *framework.Env, ns, name string) {
	t.Helper()
	patch := `[{"op":"replace","path":"/spec/template/spec/containers/0/readinessProbe/httpGet/path","value":"/e2e-not-ready"}]`
	_, err := e.Dynamic.Resource(framework.RolloutGVR).Namespace(ns).Patch(context.Background(), name,
		types.JSONPatchType, []byte(patch), metav1.PatchOptions{})
	require.NoError(t, err, "break the readiness probe of Rollout %s/%s", ns, name)
}

// TestFlagger_PromotesBundleRevision checks delivery.delegate: flagger: the
// step waits while Flagger analyzes the new revision and is Verified once
// Flagger promoted it to the primary Deployment.
//
// Covers HEALTH-FLAG-01, DELIVERY-02.
func TestFlagger_PromotesBundleRevision(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newCanaryApp(t, e, "prod")
	a.apply(t, a.deliveryPipeline(flagger, true, nil))
	v2 := fixtures.Image + ":" + fixtures.V2

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", v2)
	ps := e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, "prod", "HealthChecking", deliveryTimeout,
		"waiting for flagger: Canary phase: Progressing")
	assert.Zero(t, ps.Status.ConsecutiveHealthFailures)
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.PrimaryWorkload("prod")),
		"the primary serves V1 during the analysis")

	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", deliveryTimeout)
	assert.Contains(t, ps.Status.Message, "health check passed via flagger: Canary phase: Succeeded; primary Deployment "+
		a.ns+"/"+fixtures.PrimaryWorkload("prod")+" runs the Bundle images")
	assert.Equal(t, v2, e.DeploymentImage(t, a.ns, fixtures.PrimaryWorkload("prod")))
	c, err := e.GetCanary(context.Background(), a.ns, fixtures.Workload("prod"))
	require.NoError(t, err)
	assert.Equal(t, "Succeeded", c.Phase)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// TestFlagger_StaleSucceededDoesNotVerify checks health.type: flagger on a
// broken release after a good one. Until Argo CD applies it, the Canary is
// Succeeded from the good release, which must not verify the broken one (bug
// 2). Flagger then fails the analysis, and the step fails at once, long before
// health.timeout, with Flagger's reason. The primary stays on the good release.
//
// Covers HEALTH-FLAG-02, HEALTH-FLAG-03.
func TestFlagger_StaleSucceededDoesNotVerify(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newCanaryApp(t, e, "prod")
	a.apply(t, a.deliveryPipeline(flagger, false, func(env *v1alpha1.EnvironmentSpec) { env.Health.Timeout = "10m" }))
	canary, v2 := fixtures.Workload("prod"), fixtures.Image+":"+fixtures.V2

	good := e.CreateBundle(t, a.ns, pipelineName, "--image", v2)
	e.WaitStepState(t, a.ns, pipelineName, good, "prod", "Verified", deliveryTimeout)

	bundle := a.holdOldRevision(t, flagger, "prod", fixtures.Image+":"+fixtures.BrokenTag,
		"a Succeeded Canary on V2 does not verify the broken release")

	e.SetArgoAutoSync(t, a.argoApp("prod"), true)
	c := e.WaitCanaryPhase(t, a.ns, canary, "Failed", deliveryTimeout)
	failedAt := time.Now()
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", time.Minute)
	t.Logf("step failed %s after the Canary", time.Since(failedAt).Round(time.Second))
	require.NotEmpty(t, c.Message, "Flagger says why the analysis failed")
	assert.Equal(t, "health alarm via flagger (onHealthFailure=none): Canary phase: Failed — "+c.Message, ps.Status.Message)
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	assert.Equal(t, v2, e.DeploymentImage(t, a.ns, fixtures.PrimaryWorkload("prod")), "Flagger kept V2 on the primary")
}

// TestFlagger_StaleFailedDoesNotFail checks a good release after a broken
// one: until Argo CD applies it, and until Flagger analyzes it, the Canary is
// Failed from the broken release, which must not fail the good one (bug 3).
// It is Verified once Flagger promotes it.
//
// Covers HEALTH-FLAG-04.
func TestFlagger_StaleFailedDoesNotFail(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newCanaryApp(t, e, "prod")
	a.apply(t, a.deliveryPipeline(flagger, false, nil))
	canary, v2 := fixtures.Workload("prod"), fixtures.Image+":"+fixtures.V2

	first := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
	e.WaitStepState(t, a.ns, pipelineName, first, "prod", "Failed", deliveryTimeout)
	e.WaitCanaryPhase(t, a.ns, canary, "Failed", time.Second)

	bundle := a.holdOldRevision(t, flagger, "prod", v2, "a Failed Canary of the broken release does not fail V2")

	e.SetArgoAutoSync(t, a.argoApp("prod"), true)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", deliveryTimeout)
	assert.Contains(t, ps.Status.Message, "health check passed via flagger: Canary phase: Succeeded")
	assert.Equal(t, v2, e.DeploymentImage(t, a.ns, fixtures.PrimaryWorkload("prod")))
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// TestDelivery_FailureAppliesOnHealthFailure checks that a delegated
// delivery's failure applies onHealthFailure: abort. An aborted Rollout is
// Degraded, which is unhealthy, so the step is AbortedByAlarm at
// health.timeout; a Failed Canary is terminal, so at once.
//
// Covers DELIVERY-03.
func TestDelivery_FailureAppliesOnHealthFailure(t *testing.T) {
	t.Parallel()
	broken := fixtures.Image + ":" + fixtures.BrokenTag

	t.Run("Rollout", func(t *testing.T) {
		t.Parallel()
		e := framework.New(t)
		a := newRolloutApp(t, e, "10s", "prod")
		a.apply(t, a.deliveryPipeline(rollouts, true, func(env *v1alpha1.EnvironmentSpec) {
			env.Health.Timeout = "90s"
			env.OnHealthFailure = "abort"
		}))
		bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", broken)
		e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, "prod", "HealthChecking", deliveryTimeout,
			"unhealthy via argoRollouts: Rollout phase: Degraded")
		ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "AbortedByAlarm", 2*time.Minute)
		assert.Contains(t, ps.Status.Message, "health alarm via argoRollouts (onHealthFailure=abort): health check timeout after 1m30s; "+
			"last result: unhealthy via argoRollouts: Rollout phase: Degraded")
		assert.True(t, strings.HasSuffix(ps.Status.Message, "— human intervention required"), ps.Status.Message)
		e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	})

	t.Run("Canary", func(t *testing.T) {
		t.Parallel()
		e := framework.New(t)
		a := newCanaryApp(t, e, "prod")
		a.apply(t, a.deliveryPipeline(flagger, true, func(env *v1alpha1.EnvironmentSpec) {
			env.Health.Timeout = "10m"
			env.OnHealthFailure = "abort"
		}))
		bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", broken)
		c := e.WaitCanaryPhase(t, a.ns, fixtures.Workload("prod"), "Failed", deliveryTimeout)
		ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "AbortedByAlarm", time.Minute)
		assert.Equal(t, "health alarm via flagger (onHealthFailure=abort): Canary phase: Failed — "+c.Message+
			" — human intervention required", ps.Status.Message)
		e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	})
}

// rollbackCreated is how a step that applied onHealthFailure: rollback names
// the rollback Bundle.
var rollbackCreated = regexp.MustCompile(`rollback Bundle (\S+) created$`)

// exampleRollback runs the rollback journey of the Rollouts and Flagger
// examples on p's single pr-review environment prod: V2 is promoted through
// its PR and Verified; a broken release merged next fails its health check
// with failure, the step is RollingBack, and the rollback Bundle's PR, once
// merged, restores V2. It returns the broken and rollback Bundles.
func (a *app) exampleRollback(t *testing.T, p *v1alpha1.Pipeline, adapter, failure string) (string, string) {
	t.Helper()
	e := a.e
	require.Equal(t, "pr-review", p.Spec.Environments[0].Approval)
	require.Equal(t, "rollback", p.Spec.Environments[0].OnHealthFailure, "the example rolls back a failed release")
	a.apply(t, p)
	ctx := context.Background()
	merge := func(bundle, what string) gitserver.PR {
		e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
		pr := e.WaitPR(t, a.repo, time.Minute, what, func(pr gitserver.PR) bool {
			return pr.State == "open" && strings.Contains(pr.Body, bundle)
		})
		require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
		return pr
	}

	good := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	merge(good, "the V2 PR")
	e.WaitStepState(t, a.ns, pipelineName, good, "prod", "Verified", deliveryTimeout)

	broken := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
	merge(broken, "the broken release's PR")
	ps := e.WaitStepState(t, a.ns, pipelineName, broken, "prod", "RollingBack", deliveryTimeout)
	assert.Contains(t, ps.Status.Message, "health alarm via "+adapter+" (onHealthFailure=rollback): "+failure)
	m := rollbackCreated.FindStringSubmatch(ps.Status.Message)
	require.NotNil(t, m, "the step names the rollback Bundle: %s", ps.Status.Message)
	rollback := m[1]
	// The rollback Bundle is newer and the broken one was still Promoting, so
	// it supersedes it (docs/concepts.md, Bundle supersession).
	e.WaitBundlePhase(t, a.ns, broken, "Superseded", time.Minute)

	pr := merge(rollback, "the rollback PR")
	assert.Subset(t, pr.Labels, []string{"kardinal", "kardinal/promotion", "kardinal/rollback"})
	assert.Contains(t, pr.Body, fixtures.V2, "the rollback restores V2")
	e.WaitStepState(t, a.ns, pipelineName, rollback, "prod", "Verified", deliveryTimeout)
	e.WaitBundlePhase(t, a.ns, rollback, "Verified", time.Minute)

	// The RollingBack step is over: history shows how long it took, not "...".
	history := e.MustKardinal(t, a.ns, "history", pipelineName)
	rows := map[string][]string{}
	for _, line := range strings.Split(history, "\n") {
		if f := strings.Fields(line); len(f) >= 5 {
			rows[f[0]] = f
		}
	}
	require.Contains(t, rows, broken, history)
	require.Contains(t, rows, rollback, history)
	assert.NotEqual(t, "...", rows[broken][4], "the RollingBack step has ended:\n%s", history)
	assert.Equal(t, "rollback", rows[rollback][1], history)
	return broken, rollback
}

// TestRollouts_ExampleDegradedOpensRollbackPR runs the prod environment of
// examples/argo-rollouts-demo: a Rollout that Argo Rollouts aborts to
// Degraded fails its health check at health.timeout, and onHealthFailure:
// rollback opens a rollback PR that restores the previous release. The test
// shortens the example's 30m health.timeout.
//
// Covers EX-ROLLOUTS-01.
func TestRollouts_ExampleDegradedOpensRollbackPR(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newRolloutApp(t, e, "10s", "prod")
	p := a.examplePipeline(t, "examples/argo-rollouts-demo/pipeline.yaml", "prod", func(env *v1alpha1.EnvironmentSpec) {
		require.Equal(t, rollouts, env.Health.Type)
		env.Health.Timeout = "2m"
	})
	a.exampleRollback(t, p, rollouts,
		"health check timeout after 2m0s; last result: unhealthy via argoRollouts: Rollout phase: Degraded")
	e.WaitRolloutHealthy(t, a.ns, fixtures.Workload("prod"), fixtures.Image+":"+fixtures.V2, time.Minute)
}

// TestFlagger_ExampleFailedCanaryOpensRollbackPR runs the prod environment of
// examples/flagger-demo as written (30m health.timeout): a Canary whose
// analysis fails rolls the step back at once, and the rollback PR restores the
// previous release.
//
// Covers EX-FLAGGER-01.
func TestFlagger_ExampleFailedCanaryOpensRollbackPR(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newCanaryApp(t, e, "prod")
	p := a.examplePipeline(t, "examples/flagger-demo/pipeline.yaml", "prod", func(env *v1alpha1.EnvironmentSpec) {
		require.Equal(t, flagger, env.Delivery.Delegate)
		require.Equal(t, "30m", env.Health.Timeout)
	})
	a.exampleRollback(t, p, flagger, "Canary phase: Failed — ")
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.PrimaryWorkload("prod")))
}
