//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// healthTimeout is health.timeout for Pipelines whose test is not about the
// timeout: longer than a rollout, shorter than a test.
const healthTimeout = "3m"

// resourcePipeline is a.pipeline with resource health on each environment's
// Deployment, fixtures.Workload(env) in the test namespace. Argo CD still
// delivers; kardinal reads the Deployment.
func (a *app) resourcePipeline() *v1alpha1.Pipeline {
	p := a.pipeline(nil)
	for i := range p.Spec.Environments {
		env := &p.Spec.Environments[i]
		env.Health = v1alpha1.HealthConfig{Type: "resource", Timeout: healthTimeout,
			Resource: &v1alpha1.ResourceRef{Name: fixtures.Workload(env.Name), Namespace: a.ns}}
	}
	return p
}

// envSpec returns p's environment name for changing it before a.apply.
func envSpec(t *testing.T, p *v1alpha1.Pipeline, name string) *v1alpha1.EnvironmentSpec {
	t.Helper()
	for i := range p.Spec.Environments {
		if p.Spec.Environments[i].Name == name {
			return &p.Spec.Environments[i]
		}
	}
	t.Fatalf("Pipeline %s has no environment %s", p.Name, name)
	return nil
}

// verifiedAt is when the step's Verified condition was set.
func verifiedAt(t *testing.T, ps *v1alpha1.PromotionStep) (time.Time, *metav1.Condition) {
	t.Helper()
	c := meta.FindStatusCondition(ps.Status.Conditions, "Verified")
	require.NotNil(t, c, "a Verified step has a Verified condition: %v", ps.Status.Conditions)
	return c.LastTransitionTime.Time, c
}

// imageV2 is the image every health test promotes over fixtures.V1.
var imageV2 = fixtures.Image + ":" + fixtures.V2

// TestHealth_ResourceDefaults checks the resource adapter with no health
// config at all: the type defaults to resource, and the adapter reads the
// Deployment named after the Pipeline in the namespace named after the
// environment, for condition Available. An unknown type is rejected by the
// API server. Covers HEALTH-RES-01, HEALTH-UNKNOWN-01.
func TestHealth_ResourceDefaults(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	// The environment is named after the namespace and the Pipeline after the
	// Deployment, so the defaults find the workload.
	env, name := ns, fixtures.Workload(ns)
	repo := e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{env}}))
	e.ArgoApp(t, ns, repo, fixtures.Path(env), ns)
	e.WaitArgoApp(t, ns, syncTimeout)
	e.WaitDeploymentImage(t, ns, name, fixtures.Image+":"+fixtures.V1, syncTimeout)

	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: repo.CloneURL, Branch: repo.Branch,
				SecretRef: &v1alpha1.SecretRef{Name: framework.GitSecretName}},
			Environments: []v1alpha1.EnvironmentSpec{{Name: env, Path: fixtures.Path(env),
				Update: v1alpha1.UpdateConfig{Strategy: "kustomize"}, Health: v1alpha1.HealthConfig{Type: "bogus"}}},
		},
	}
	err := e.Client.Create(context.Background(), p.DeepCopy())
	require.Error(t, err, "health.type bogus must be rejected")
	assert.Contains(t, err.Error(), `Unsupported value: "bogus"`)
	assert.Contains(t, err.Error(), `"resource", "argocd", "flux", "argoRollouts", "flagger"`)

	p.Spec.Environments[0].Health = v1alpha1.HealthConfig{}
	require.NoError(t, e.Client.Create(context.Background(), p))
	bundle := e.CreateBundle(t, ns, name, "--image", imageV2)
	ps := e.WaitStepState(t, ns, name, bundle, env, "Verified", promoteTimeout)
	assert.Equal(t, "health check passed via resource: Available=True: Deployment has minimum availability., "+
		"1/1 replicas updated and available", ps.Status.Message)
	_, c := verifiedAt(t, ps)
	assert.Equal(t, "promotion complete", c.Message)
	assert.Contains(t, e.ReadFile(t, repo, repo.Branch, fixtures.Path(env)+"/kustomization.yaml"), "newTag: "+fixtures.V2)
	assert.Equal(t, imageV2, e.DeploymentImage(t, ns, name))
	e.WaitBundlePhase(t, ns, bundle, "Verified", time.Minute)
}

// TestHealth_ResourceWaitsForImage checks that the resource adapter does not
// pass a Deployment still running the old image: with Argo CD auto-sync off,
// git has the new tag but the cluster does not, and the step waits (not a
// failure) until the Deployment runs the Bundle's image. Covers HEALTH-RES-02.
func TestHealth_ResourceWaitsForImage(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	e.SetArgoAutoSync(t, a.argoApp("test"), false)
	a.apply(t, a.resourcePipeline())

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	waiting := fmt.Sprintf("waiting for resource: Deployment %s/%s not updated yet: runs %s:%s, Bundle has %s",
		a.ns, fixtures.Workload("test"), fixtures.Image, fixtures.V1, imageV2)
	e.WaitStep(t, a.ns, pipelineName, bundle, "test", promoteTimeout, "wait for the new image",
		func(ps *v1alpha1.PromotionStep) bool { return ps.Status.Message == waiting })
	assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/kustomization.yaml"),
		"newTag: "+fixtures.V2, "the change is in git")
	framework.Consistently(t, 25*time.Second, "the step waits while the old image runs", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return ps.Status.State == "HealthChecking" && ps.Status.Message == waiting &&
			ps.Status.ConsecutiveHealthFailures == 0, framework.DescribeStep(ps)
	})
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))

	e.SetArgoAutoSync(t, a.argoApp("test"), true)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.True(t, strings.HasPrefix(ps.Status.Message, "health check passed via resource: Available=True"), ps.Status.Message)
	assert.Equal(t, imageV2, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
}

// TestHealth_ResourceOverrides checks health.resource name, namespace and
// condition: the Pipeline name (podinfo) and environment name (test) name no
// Deployment, so only the overrides find podinfo-test in the test namespace,
// and the check reads the Progressing condition instead of Available.
// Covers HEALTH-RES-05.
func TestHealth_ResourceOverrides(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	p := a.resourcePipeline()
	envSpec(t, p, "test").Health.Resource.Condition = "Progressing"
	a.apply(t, p)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.True(t, strings.HasPrefix(ps.Status.Message, "health check passed via resource: Progressing=True: ReplicaSet "), ps.Status.Message)
	assert.True(t, strings.HasSuffix(ps.Status.Message, "has successfully progressed., 1/1 replicas updated and available"), ps.Status.Message)
	assert.Equal(t, imageV2, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
}

// TestHealth_LabelSelector checks health.labelSelector: in test, two
// Deployments match, and the step waits until both run the Bundle image
// (the second is not in git; the test updates it by hand). In prod, no
// Deployment matches, which is unhealthy until health.timeout fails the
// step. Covers HEALTH-RES-04.
func TestHealth_LabelSelector(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	selector := map[string]string{"app.kubernetes.io/name": fixtures.Workload("test")}
	extra := fixtures.PodinfoDeployment(a.ns, fixtures.Workload("test")+"-extra", fixtures.Image+":"+fixtures.V1, selector)
	require.NoError(t, e.Client.Create(context.Background(), extra))
	e.WaitDeploymentImage(t, a.ns, extra.Name, fixtures.Image+":"+fixtures.V1, syncTimeout)

	p := a.resourcePipeline()
	envSpec(t, p, "test").Health = v1alpha1.HealthConfig{Type: "resource", Timeout: healthTimeout,
		LabelSelector: selector, Resource: &v1alpha1.ResourceRef{Namespace: a.ns}}
	none := map[string]string{"app.kubernetes.io/name": "podinfo-none"}
	envSpec(t, p, "prod").Health = v1alpha1.HealthConfig{Type: "resource", Timeout: "1m",
		LabelSelector: none, Resource: &v1alpha1.ResourceRef{Namespace: a.ns}}
	a.apply(t, p)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("test"), imageV2, promoteTimeout)
	waiting := fmt.Sprintf("waiting for resource: Deployment %s/%s not updated yet: runs %s:%s, Bundle has %s",
		a.ns, extra.Name, fixtures.Image, fixtures.V1, imageV2)
	e.WaitStep(t, a.ns, pipelineName, bundle, "test", time.Minute, "wait for the second Deployment",
		func(ps *v1alpha1.PromotionStep) bool { return ps.Status.Message == waiting })
	framework.Consistently(t, 15*time.Second, "one matching Deployment on the old image holds the step", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return ps.Status.State == "HealthChecking" && ps.Status.Message == waiting, framework.DescribeStep(ps)
	})

	var d appsv1.Deployment
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: a.ns, Name: extra.Name}, &d))
	patched := d.DeepCopy()
	patched.Spec.Template.Spec.Containers[0].Image = imageV2
	require.NoError(t, e.Client.Patch(context.Background(), patched, client.MergeFrom(&d)))
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, fmt.Sprintf("health check passed via resource: 2 Deployments matching %v rolled out and Available", selector),
		ps.Status.Message)

	unhealthy := fmt.Sprintf("unhealthy via resource: no Deployment in namespace %s matches labels %v", a.ns, none)
	e.WaitStep(t, a.ns, pipelineName, bundle, "prod", promoteTimeout, "no match is unhealthy",
		func(ps *v1alpha1.PromotionStep) bool {
			return ps.Status.State == "HealthChecking" && ps.Status.Message == unhealthy && ps.Status.ConsecutiveHealthFailures >= 1
		})
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", 2*time.Minute)
	assert.Equal(t, "health alarm via resource (onHealthFailure=none): health check timeout after 1m0s; last result: "+unhealthy,
		ps.Status.Message)
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
}

// TestHealth_UnsupportedKind checks health.resource.kind other than
// Deployment: the Pipeline reports it (Ready=False, NotImplemented), and the
// step fails with the documented message before it changes git, so the
// environment keeps its version and no commit or PR exists.
// Covers HEALTH-RES-06.
func TestHealth_UnsupportedKind(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	p := a.resourcePipeline()
	envSpec(t, p, "test").Health.Resource.Kind = "StatefulSet"
	a.apply(t, p)

	want := `health.resource.kind "StatefulSet" is not supported: only Deployment is checked`
	framework.Eventually(t, time.Minute, "the Pipeline to report the field", func(ctx context.Context) (bool, string) {
		var got v1alpha1.Pipeline
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &got); err != nil {
			return false, err.Error()
		}
		c := meta.FindStatusCondition(got.Status.Conditions, "Ready")
		if c == nil {
			return false, "no Ready condition"
		}
		return c.Status == metav1.ConditionFalse && c.Reason == "NotImplemented" &&
			strings.Contains(c.Message, `environment "test": `+want), fmt.Sprintf("%s/%s: %s", c.Status, c.Reason, c.Message)
	})

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", promoteTimeout)
	assert.Equal(t, want, ps.Status.Message)
	assert.Empty(t, ps.Status.Outputs["commitSHA"], "the step failed before it pushed")
	assert.Nil(t, ps.Status.HealthCheckExpiry, "the step never reached the health check")
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/kustomization.yaml"), "newTag: "+fixtures.V1)
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
	prs, err := e.Git.PullRequests(context.Background(), a.repo)
	require.NoError(t, err)
	assert.Empty(t, prs)
}

// TestHealth_TimeoutFails checks health.timeout: unset, the expiry is 10m
// after the check starts (test); set to 1m on a check that never passes
// (prod reads a condition Deployments do not have), each check counts one
// more consecutive failure, and the expiry fails the step with the last
// result in the message. Covers HEALTH-TIMEOUT-01, HEALTH-FAILCOUNT-01.
func TestHealth_TimeoutFails(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	p := a.resourcePipeline()
	envSpec(t, p, "test").Health.Timeout = ""
	prod := envSpec(t, p, "prod")
	prod.Health.Timeout = "1m"
	prod.Health.Resource.Condition = "Ready"
	a.apply(t, p)

	start := time.Now()
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	require.NotNil(t, ps.Status.HealthCheckExpiry)
	verified, _ := verifiedAt(t, ps)
	expiry := ps.Status.HealthCheckExpiry.Time
	assert.False(t, expiry.Before(start.Add(10*time.Minute-time.Second)), "default timeout 10m: expiry %s, test started %s", expiry, start)
	assert.False(t, expiry.After(verified.Add(10*time.Minute+time.Second)), "default timeout 10m: expiry %s, Verified %s", expiry, verified)

	unhealthy := fmt.Sprintf(`unhealthy via resource: Deployment %s/%s: condition "Ready" not found`, a.ns, fixtures.Workload("prod"))
	first := e.WaitStep(t, a.ns, pipelineName, bundle, "prod", promoteTimeout, "an unhealthy check",
		func(ps *v1alpha1.PromotionStep) bool {
			return ps.Status.State == "HealthChecking" && ps.Status.Message == unhealthy && ps.Status.ConsecutiveHealthFailures >= 1
		})
	n := first.Status.ConsecutiveHealthFailures
	e.WaitStep(t, a.ns, pipelineName, bundle, "prod", 30*time.Second, "the next check to count one more failure",
		func(ps *v1alpha1.PromotionStep) bool {
			return ps.Status.State == "HealthChecking" && ps.Status.ConsecutiveHealthFailures == n+1
		})
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", 2*time.Minute)
	assert.Equal(t, "health alarm via resource (onHealthFailure=none): health check timeout after 1m0s; last result: "+unhealthy,
		ps.Status.Message)
	require.NotNil(t, ps.Status.HealthCheckExpiry)
	assert.False(t, ps.Status.HealthCheckExpiry.Time.Before(verified.Add(time.Minute-time.Second)), "prod expiry 1m after its check started")
	// About one failure per 10s check over 1m, plus the timeout itself.
	assert.GreaterOrEqual(t, ps.Status.ConsecutiveHealthFailures, 4)
	assert.Equal(t, imageV2, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")), "prod runs the new version; only the check failed")
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
}

// brokenRollout promotes fixtures.BrokenTag to test with onHealthFailure
// policy and returns the Bundle. The new pod never pulls, so after the
// Deployment's 60s progress deadline the rollout is ProgressDeadlineExceeded.
func brokenRollout(t *testing.T, e *framework.Env, policy string) (*app, string) {
	t.Helper()
	a := newArgoApp(t, e, "test")
	p := a.resourcePipeline()
	test := envSpec(t, p, "test")
	test.OnHealthFailure = policy
	test.Health.Timeout = "5m" // longer than the progress deadline
	a.apply(t, p)
	return a, e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
}

// TestHealth_ProgressDeadlineFails checks that ProgressDeadlineExceeded is a
// terminal result: it fails the step on the first failed check, long before
// health.timeout, and onHealthFailure unset (none) leaves it Failed with no
// rollback. Covers HEALTH-RES-03, ONFAIL-NONE-01.
func TestHealth_ProgressDeadlineFails(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a, bundle := brokenRollout(t, e, "")

	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", promoteTimeout)
	prefix := fmt.Sprintf("health alarm via resource (onHealthFailure=none): Deployment %s/%s rollout failed: ProgressDeadlineExceeded: ",
		a.ns, fixtures.Workload("test"))
	assert.True(t, strings.HasPrefix(ps.Status.Message, prefix), ps.Status.Message)
	assert.Contains(t, ps.Status.Message, "has timed out progressing")
	assert.NotContains(t, ps.Status.Message, "timeout after")
	assert.Equal(t, 1, ps.Status.ConsecutiveHealthFailures, "a terminal result fails on the first failed check")
	require.NotNil(t, ps.Status.LastHealthCheckAt)
	require.NotNil(t, ps.Status.HealthCheckExpiry)
	assert.True(t, ps.Status.LastHealthCheckAt.Before(ps.Status.HealthCheckExpiry), "failed before health.timeout")
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/kustomization.yaml"), "newTag: "+fixtures.BrokenTag)

	framework.Consistently(t, 20*time.Second, "the step stays Failed and nothing rolls back", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		var bundles v1alpha1.BundleList
		if err := e.Client.List(ctx, &bundles, client.InNamespace(a.ns)); err != nil {
			return false, err.Error()
		}
		return ps.Status.State == "Failed" && len(bundles.Items) == 1,
			fmt.Sprintf("state=%s, %d Bundles", ps.Status.State, len(bundles.Items))
	})
}

// TestHealth_AbortStaysAborted checks onHealthFailure: abort: the failed
// rollout stops the step at AbortedByAlarm with a call for a human, and the
// step does not go back to Pending or retry. Covers ONFAIL-ABORT-01.
func TestHealth_AbortStaysAborted(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a, bundle := brokenRollout(t, e, "abort")

	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "AbortedByAlarm", promoteTimeout)
	assert.True(t, strings.HasPrefix(ps.Status.Message, fmt.Sprintf(
		"health alarm via resource (onHealthFailure=abort): Deployment %s/%s rollout failed: ProgressDeadlineExceeded: ",
		a.ns, fixtures.Workload("test"))), ps.Status.Message)
	assert.True(t, strings.HasSuffix(ps.Status.Message, " — human intervention required"), ps.Status.Message)
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)

	framework.Consistently(t, 30*time.Second, "the step stays AbortedByAlarm", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		var bundles v1alpha1.BundleList
		if err := e.Client.List(ctx, &bundles, client.InNamespace(a.ns)); err != nil {
			return false, err.Error()
		}
		return ps.Status.State == "AbortedByAlarm" && len(bundles.Items) == 1,
			fmt.Sprintf("state=%s, %d Bundles", ps.Status.State, len(bundles.Items))
	})
}

// bakePipeline applies a resource-health Pipeline for test with a bake
// window and creates the Bundle.
func bakePipeline(t *testing.T, e *framework.Env, bake v1alpha1.BakeConfig) (*app, string) {
	t.Helper()
	a := newArgoApp(t, e, "test")
	p := a.resourcePipeline()
	envSpec(t, p, "test").Bake = &bake
	a.apply(t, p)
	return a, e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
}

// waitBakeStarted waits for the first healthy check, which starts the window.
func waitBakeStarted(t *testing.T, e *framework.Env, a *app, bundle string, minutes int) *v1alpha1.PromotionStep {
	t.Helper()
	started := fmt.Sprintf("bake: 0m/%dm contiguous healthy via resource (~%dm remaining, resets=0)", minutes, minutes)
	return e.WaitStep(t, a.ns, pipelineName, bundle, "test", promoteTimeout, "the bake window to start",
		func(ps *v1alpha1.PromotionStep) bool {
			return ps.Status.State == "HealthChecking" && ps.Status.BakeStartedAt != nil && ps.Status.Message == started
		})
}

// TestHealth_BakeCompletes checks bake.minutes: the step stays in
// HealthChecking for the whole window after the first healthy check, then
// becomes Verified with the bake message and a BakeComplete condition.
// Covers BAKE-01.
func TestHealth_BakeCompletes(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a, bundle := bakePipeline(t, e, v1alpha1.BakeConfig{Minutes: 1})

	started := waitBakeStarted(t, e, a, bundle, 1).Status.BakeStartedAt.Time
	assert.Equal(t, imageV2, e.DeploymentImage(t, a.ns, fixtures.Workload("test")), "the window starts once the new image is healthy")
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", 3*time.Minute)
	assert.Equal(t, "bake complete: 1m contiguous healthy via resource (resets=0)", ps.Status.Message)
	at, c := verifiedAt(t, ps)
	assert.Equal(t, "BakeComplete", c.Reason)
	assert.Equal(t, "contiguous soak 1m complete", c.Message)
	// Timestamps have second precision.
	assert.GreaterOrEqual(t, at.Sub(started), time.Minute-time.Second, "Verified %s, bake started %s", at, started)
	assert.Equal(t, 0, ps.Status.BakeResets)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// TestHealth_BakeResetOnAlarm checks bake.policy reset-on-alarm (the
// default): the pod turns unready during the window, and each unhealthy check
// restarts the window and counts a reset instead of failing. Once the pod is
// ready again, a full window completes the bake. Covers BAKE-02.
func TestHealth_BakeResetOnAlarm(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a, bundle := bakePipeline(t, e, v1alpha1.BakeConfig{Minutes: 2})
	pods := map[string]string{"app.kubernetes.io/name": fixtures.Workload("test")}

	waitBakeStarted(t, e, a, bundle, 2)
	e.SetReadyz(t, a.ns, pods, false)
	alarm := fmt.Sprintf("need 2m contiguous): Deployment %s/%s: 0 of 1 updated replicas available (Available=False",
		a.ns, fixtures.Workload("test"))
	ps := e.WaitStep(t, a.ns, pipelineName, bundle, "test", time.Minute, "the alarm to reset the window",
		func(ps *v1alpha1.PromotionStep) bool {
			return ps.Status.BakeResets >= 1 && strings.Contains(ps.Status.Message, alarm)
		})
	assert.True(t, strings.HasPrefix(ps.Status.Message, fmt.Sprintf(
		"bake: health alarm via resource — timer reset (resets=%d, ", ps.Status.BakeResets)), ps.Status.Message)
	framework.Consistently(t, 20*time.Second, "an alarm during the bake does not fail the step", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return ps.Status.State == "HealthChecking", framework.DescribeStep(ps)
	})
	resets := e.MustStep(t, a.ns, pipelineName, bundle, "test").Status.BakeResets
	assert.Greater(t, resets, ps.Status.BakeResets, "every unhealthy check resets the window")

	e.SetReadyz(t, a.ns, pods, true)
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", 4*time.Minute)
	assert.Equal(t, fmt.Sprintf("bake complete: 2m contiguous healthy via resource (resets=%d)", ps.Status.BakeResets), ps.Status.Message)
	assert.GreaterOrEqual(t, ps.Status.BakeResets, resets)
	at, _ := verifiedAt(t, ps)
	assert.GreaterOrEqual(t, at.Sub(ps.Status.BakeStartedAt.Time), 2*time.Minute-time.Second,
		"a full window after the last reset")
}

// TestHealth_BakeFailOnAlarm checks bake.policy fail-on-alarm: the first
// unhealthy check during the window applies onHealthFailure (none: Failed)
// with no reset. Covers BAKE-03.
func TestHealth_BakeFailOnAlarm(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a, bundle := bakePipeline(t, e, v1alpha1.BakeConfig{Minutes: 2, Policy: "fail-on-alarm"})

	started := waitBakeStarted(t, e, a, bundle, 2).Status.BakeStartedAt.Time
	e.SetReadyz(t, a.ns, map[string]string{"app.kubernetes.io/name": fixtures.Workload("test")}, false)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", time.Minute)
	assert.True(t, strings.HasPrefix(ps.Status.Message, fmt.Sprintf(
		"health alarm via resource (onHealthFailure=none): Deployment %s/%s: 0 of 1 updated replicas available (Available=False",
		a.ns, fixtures.Workload("test"))), ps.Status.Message)
	assert.Equal(t, 0, ps.Status.BakeResets)
	require.NotNil(t, ps.Status.LastHealthCheckAt)
	assert.Less(t, ps.Status.LastHealthCheckAt.Sub(started), 2*time.Minute, "failed inside the window")
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
}
