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
	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// healthTimeout is health.timeout for Pipelines whose test is not about the
// timeout: longer than a rollout, shorter than a test.
const healthTimeout = "3m"

// resourcePipeline is a.pipeline(approval) with resource health on each
// environment's Deployment, fixtures.Workload(env) in the test namespace.
// Argo CD still delivers; kardinal reads the Deployment. A namespace-mode
// controller caches only its watch namespace, so it needs this: it cannot
// read Argo CD Applications in argocd.
func (a *app) resourcePipeline(approval map[string]string) *v1alpha1.Pipeline {
	p := a.pipeline(approval)
	for i := range p.Spec.Environments {
		env := &p.Spec.Environments[i]
		env.Health = v1alpha1.HealthConfig{Type: "resource", Timeout: healthTimeout,
			Resource: &v1alpha1.ResourceRef{Name: fixtures.Workload(env.Name), Namespace: a.ns}}
	}
	return p
}

// verifiedCondition is the step's Verified condition and when it was set.
func verifiedCondition(t *testing.T, ps *v1alpha1.PromotionStep) (time.Time, *metav1.Condition) {
	t.Helper()
	c := meta.FindStatusCondition(ps.Status.Conditions, "Verified")
	require.NotNil(t, c, "a Verified step has a Verified condition: %v", ps.Status.Conditions)
	return c.LastTransitionTime.Time, c
}

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
	_, c := verifiedCondition(t, ps)
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
	a.apply(t, a.resourcePipeline(nil))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	waiting := fmt.Sprintf("waiting for resource: Deployment %s/%s not updated yet: runs %s:%s, Bundle has %s",
		a.ns, fixtures.Workload("test"), fixtures.Image, fixtures.V1, imageV2)
	e.WaitStep(t, a.ns, pipelineName, bundle, "test", promoteTimeout, "wait for the new image",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.Message == waiting, framework.DescribeStep(ps)
		})
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
	p := a.resourcePipeline(nil)
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
	// extra is a second Deployment outside git that selector matches: its pods
	// keep their own app.kubernetes.io/name, the Deployment carries test's.
	extra := fixtures.Workload("test") + "-extra"
	applyExtra := func(image string) {
		t.Helper()
		var obj unstructured.Unstructured
		require.NoError(t, yaml.Unmarshal([]byte(fixtures.Deployment(extra, image)), &obj.Object))
		obj.SetNamespace(a.ns)
		obj.SetLabels(selector)
		_, err := e.Apply(context.Background(), &obj)
		require.NoError(t, err)
	}
	applyExtra(fixtures.Image + ":" + fixtures.V1)
	e.WaitDeploymentImage(t, a.ns, extra, fixtures.Image+":"+fixtures.V1, syncTimeout)

	p := a.resourcePipeline(nil)
	envSpec(t, p, "test").Health = v1alpha1.HealthConfig{Type: "resource", Timeout: healthTimeout,
		LabelSelector: selector, Resource: &v1alpha1.ResourceRef{Namespace: a.ns}}
	none := map[string]string{"app.kubernetes.io/name": "podinfo-none"}
	envSpec(t, p, "prod").Health = v1alpha1.HealthConfig{Type: "resource", Timeout: "1m",
		LabelSelector: none, Resource: &v1alpha1.ResourceRef{Namespace: a.ns}}
	a.apply(t, p)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("test"), imageV2, promoteTimeout)
	waiting := fmt.Sprintf("waiting for resource: Deployment %s/%s not updated yet: runs %s:%s, Bundle has %s",
		a.ns, extra, fixtures.Image, fixtures.V1, imageV2)
	e.WaitStep(t, a.ns, pipelineName, bundle, "test", time.Minute, "wait for the second Deployment",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.Message == waiting, framework.DescribeStep(ps)
		})
	framework.Consistently(t, 15*time.Second, "one matching Deployment on the old image holds the step", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return ps.Status.State == "HealthChecking" && ps.Status.Message == waiting, framework.DescribeStep(ps)
	})

	applyExtra(imageV2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, fmt.Sprintf("health check passed via resource: 2 Deployments matching %v rolled out and Available", selector),
		ps.Status.Message)

	unhealthy := fmt.Sprintf("unhealthy via resource: no Deployment in namespace %s matches labels %v", a.ns, none)
	e.WaitStep(t, a.ns, pipelineName, bundle, "prod", promoteTimeout, "no match is unhealthy",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.State == "HealthChecking" && ps.Status.Message == unhealthy && ps.Status.ConsecutiveHealthFailures >= 1, framework.DescribeStep(ps)
		})
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", 2*time.Minute)
	assert.Equal(t, "health alarm via resource (onHealthFailure=none): health check timeout after 1m0s; last result: "+unhealthy,
		ps.Status.Message)
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
}

// TestHealth_LabelSelectorStall checks a ProgressDeadlineExceeded set before
// the promotion on a Deployment of this rollout: a second Deployment that
// health.labelSelector matches already runs the Bundle image but cannot
// schedule its pod, so it stalled before the Bundle exists. Its condition
// names the ReplicaSet with the Deployment's revision, so the step fails on
// the first check, long before health.timeout, though the condition's time
// is before the health check started. Covers HEALTH-RES-08.
func TestHealth_LabelSelectorStall(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	selector := map[string]string{"app.kubernetes.io/name": fixtures.Workload("test")}
	extra := fixtures.Workload("test") + "-extra"
	var obj unstructured.Unstructured
	require.NoError(t, yaml.Unmarshal([]byte(fixtures.Deployment(extra, imageV2)), &obj.Object))
	obj.SetNamespace(a.ns)
	obj.SetLabels(selector)
	require.NoError(t, unstructured.SetNestedStringMap(obj.Object, map[string]string{"kardinal.io/e2e-no-such-node": "true"},
		"spec", "template", "spec", "nodeSelector"))
	_, err := e.Apply(context.Background(), &obj)
	require.NoError(t, err)

	var stalled string
	framework.Eventually(t, 3*time.Minute, "the second Deployment to pass its progress deadline", func(ctx context.Context) (bool, string) {
		var d appsv1.Deployment
		if err := e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: extra}, &d); err != nil {
			return false, err.Error()
		}
		for _, c := range d.Status.Conditions {
			if c.Type == appsv1.DeploymentProgressing {
				stalled = c.Message
				return c.Reason == "ProgressDeadlineExceeded", c.Reason + ": " + c.Message
			}
		}
		return false, "no Progressing condition"
	})

	p := a.resourcePipeline(nil)
	envSpec(t, p, "test").Health = v1alpha1.HealthConfig{Type: "resource", Timeout: "5m",
		LabelSelector: selector, Resource: &v1alpha1.ResourceRef{Namespace: a.ns}}
	a.apply(t, p)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", promoteTimeout)
	// The stalled pod cannot be scheduled; the reason names that too (#1365).
	assert.True(t, strings.HasPrefix(ps.Status.Message, fmt.Sprintf(
		"health alarm via resource (onHealthFailure=none): Deployment %s/%s rollout failed: ProgressDeadlineExceeded: %s; new pod %s-",
		a.ns, extra, stalled, extra)), ps.Status.Message)
	assert.Contains(t, ps.Status.Message, " is not scheduled: Unschedulable: ")
	assert.Equal(t, 1, ps.Status.ConsecutiveHealthFailures, "a terminal result fails on the first failed check")
	require.NotNil(t, ps.Status.LastHealthCheckAt)
	require.NotNil(t, ps.Status.HealthCheckExpiry)
	assert.True(t, ps.Status.LastHealthCheckAt.Add(4*time.Minute).Before(ps.Status.HealthCheckExpiry.Time),
		"failed at once, not at health.timeout: last check %s, expiry %s", ps.Status.LastHealthCheckAt, ps.Status.HealthCheckExpiry)
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
}

// TestHealth_ConfigFixOfStalledRelease checks a config Bundle promoted onto
// a Deployment whose current ReplicaSet stalled: a release of a missing image
// failed, and the config commit that fixes it pins a good image. Argo CD's
// automated sync is off, so the cluster still runs the stalled release when
// the step's health check runs. The condition names the ReplicaSet with the
// Deployment's revision, but a config Bundle may change the pod template in
// ways the check cannot see, so the condition's time decides: it is from
// before the health check started, and the step waits with no failure
// instead of failing on the stall it fixes. Once Argo CD applies the fix,
// the step is Verified. Covers HEALTH-RES-09.
func TestHealth_ConfigFixOfStalledRelease(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a, broken := brokenRollout(t, e, "")
	workload := fixtures.Workload("test")
	e.WaitStepState(t, a.ns, pipelineName, broken, "test", "Failed", promoteTimeout)
	stalled := currentReplicaSetStall(t, e, a.ns, workload)

	e.SetArgoAutoSync(t, a.argoApp("test"), false)
	kustomization := e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/kustomization.yaml")
	require.Contains(t, kustomization, "newTag: "+fixtures.BrokenTag)
	cfg := e.Repo(t, a.ns+"-config", map[string][]byte{
		fixtures.Path("test") + "/kustomization.yaml": []byte(strings.Replace(kustomization,
			"newTag: "+fixtures.BrokenTag, "newTag: "+fixtures.V2, 1)),
		fixtures.Path("test") + "/deployment.yaml": []byte(withConfigChange(fixtures.Deployment(workload, imageV2))),
	})
	commits, err := gitserver.Commits(context.Background(), e.Git, cfg, cfg.Branch, 1)
	require.NoError(t, err)
	require.NotEmpty(t, commits)
	fix := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", commits[0].SHA,
		"--config-repo", cfg.CloneURL)

	earlier := fmt.Sprintf("Deployment %s/%s: ProgressDeadlineExceeded (%s) is from an earlier rollout: its lastUpdateTime ",
		a.ns, workload, stalled)
	ps := e.WaitStepMessageAll(t, a.ns, pipelineName, fix, "test", "HealthChecking", promoteTimeout,
		earlier, " is before this health check started (", "; waiting for the Deployment controller to see this rollout progress")
	assert.Zero(t, ps.Status.ConsecutiveHealthFailures, framework.DescribeStep(ps))
	require.True(t, a.configDeployed(t, "test"), "the fix is in git")
	assert.Equal(t, stalled, currentReplicaSetStall(t, e, a.ns, workload), "Argo CD has not applied the fix")
	assert.Equal(t, fixtures.Image+":"+fixtures.BrokenTag, e.DeploymentImage(t, a.ns, workload))

	e.HoldStep(t, deliveryHold, a.ns, pipelineName, fix, "test", "the config fix waits on the stall it replaces",
		func(ps *v1alpha1.PromotionStep) bool {
			return ps.Status.State == "HealthChecking" && ps.Status.ConsecutiveHealthFailures == 0 &&
				strings.Contains(ps.Status.Message, earlier)
		})
	e.SetArgoAutoSync(t, a.argoApp("test"), true)
	ps = e.WaitStepState(t, a.ns, pipelineName, fix, "test", "Verified", promoteTimeout)
	assert.Zero(t, ps.Status.ConsecutiveHealthFailures, framework.DescribeStep(ps))
	a.waitConfigRunning(t, "test")
	assert.Equal(t, imageV2, e.DeploymentImage(t, a.ns, workload))
}

// stalledReplicaSet is the message of a Progressing condition with reason
// ProgressDeadlineExceeded.
var stalledReplicaSet = regexp.MustCompile(`^ReplicaSet "([a-z0-9.-]+)" has timed out progressing\.$`)

// currentReplicaSetStall returns the message of the Deployment's
// ProgressDeadlineExceeded after checking that the ReplicaSet it names has
// the Deployment's revision: the Deployment's current pod template stalled.
func currentReplicaSetStall(t *testing.T, e *framework.Env, ns, name string) string {
	t.Helper()
	ctx := context.Background()
	var d appsv1.Deployment
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &d))
	var prog *appsv1.DeploymentCondition
	for i := range d.Status.Conditions {
		if d.Status.Conditions[i].Type == appsv1.DeploymentProgressing {
			prog = &d.Status.Conditions[i]
		}
	}
	require.NotNil(t, prog, "Progressing condition: %v", d.Status.Conditions)
	require.Equal(t, "ProgressDeadlineExceeded", prog.Reason, prog.Message)
	m := stalledReplicaSet.FindStringSubmatch(prog.Message)
	require.NotNil(t, m, prog.Message)
	var rs appsv1.ReplicaSet
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: m[1]}, &rs))
	const revision = "deployment.kubernetes.io/revision"
	require.Equal(t, d.Annotations[revision], rs.Annotations[revision], "the stalled ReplicaSet %s is the Deployment's current one", m[1])
	return prog.Message
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
	p := a.resourcePipeline(nil)
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
//
// prod's check reads test's Deployment, which is rolled out on the Bundle's
// image before prod starts: on prod's own, the 1m timeout includes the new
// Pod's start, and under load the Pod was still ContainerCreating (the
// check waiting, not failing) when the timeout passed.
func TestHealth_TimeoutFails(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	p := a.resourcePipeline(nil)
	envSpec(t, p, "test").Health.Timeout = ""
	prod := envSpec(t, p, "prod")
	prod.Health.Timeout = "1m"
	prod.Health.Resource.Name = fixtures.Workload("test")
	prod.Health.Resource.Condition = "Ready"
	a.apply(t, p)

	start := time.Now()
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	require.NotNil(t, ps.Status.HealthCheckExpiry)
	verified, _ := verifiedCondition(t, ps)
	expiry := ps.Status.HealthCheckExpiry.Time
	assert.False(t, expiry.Before(start.Add(10*time.Minute-time.Second)), "default timeout 10m: expiry %s, test started %s", expiry, start)
	assert.False(t, expiry.After(verified.Add(10*time.Minute+time.Second)), "default timeout 10m: expiry %s, Verified %s", expiry, verified)

	unhealthy := fmt.Sprintf(`unhealthy via resource: Deployment %s/%s: condition "Ready" not found`, a.ns, fixtures.Workload("test"))
	first := e.WaitStep(t, a.ns, pipelineName, bundle, "prod", promoteTimeout, "an unhealthy check",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.State == "HealthChecking" && ps.Status.Message == unhealthy && ps.Status.ConsecutiveHealthFailures >= 1, framework.DescribeStep(ps)
		})
	n := first.Status.ConsecutiveHealthFailures
	// Checks run at most every 10s, so a later check before the 1m expiry
	// counts again; the wait ends at the expiry either way.
	next := e.WaitStep(t, a.ns, pipelineName, bundle, "prod", 2*time.Minute, "a later check to count one more failure",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.ConsecutiveHealthFailures > n || ps.Status.State != "HealthChecking", framework.DescribeStep(ps)
		})
	require.Equal(t, "HealthChecking", next.Status.State, "a later check counts before the timeout: %s", framework.DescribeStep(next))
	assert.Equal(t, unhealthy, next.Status.Message)
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", 2*time.Minute)
	assert.Equal(t, "health alarm via resource (onHealthFailure=none): health check timeout after 1m0s; last result: "+unhealthy,
		ps.Status.Message)
	require.NotNil(t, ps.Status.HealthCheckExpiry)
	assert.False(t, ps.Status.HealthCheckExpiry.Time.Before(verified.Add(time.Minute-time.Second)), "prod expiry 1m after its check started")
	// At least the unhealthy checks seen above; the timeout counts one more.
	assert.GreaterOrEqual(t, ps.Status.ConsecutiveHealthFailures, 2)
	assert.Greater(t, ps.Status.ConsecutiveHealthFailures, next.Status.ConsecutiveHealthFailures, "the timeout counts as one more failure")
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("prod"), imageV2, syncTimeout) // prod got the new version; only the check failed
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
}

// brokenRollout promotes fixtures.BrokenTag to test with onHealthFailure
// policy and returns the Bundle. The new pod never pulls, so after the
// Deployment's 60s progress deadline the rollout is ProgressDeadlineExceeded.
func brokenRollout(t *testing.T, e *framework.Env, policy string) (*app, string) {
	t.Helper()
	a := newArgoApp(t, e, "test")
	p := a.resourcePipeline(nil)
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
	assert.Regexp(t, newPodPullFailure, ps.Status.Message, "the message names why the new pod does not start")
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

// newPodPullFailure is how a health reason names the new pod of a
// fixtures.BrokenTag rollout: its podinfo container cannot pull the image.
var newPodPullFailure = regexp.MustCompile(`; new pod \S+: container podinfo is waiting: (ErrImagePull|ImagePullBackOff)\b`)

// TestHealth_TimeoutNamesPodFailure checks the resource adapter's message
// for a rollout whose new pod never starts (#1365): the image cannot be
// pulled, health.timeout (shorter than the 60s progress deadline) ends the
// wait, and the Failed message, which quotes the last result, names the new
// pod's failure, not only the old replicas that stay. Covers HEALTH-RES-10.
func TestHealth_TimeoutNamesPodFailure(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	p := a.resourcePipeline(nil)
	envSpec(t, p, "test").Health.Timeout = "40s"
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)

	waiting := e.WaitStep(t, a.ns, pipelineName, bundle, "test", promoteTimeout, "a check that names the new pod",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			ended := ps.Status.State == "Failed" || ps.Status.State == "Verified"
			return ended || newPodPullFailure.MatchString(ps.Status.Message), framework.DescribeStep(ps)
		})
	require.Equal(t, "HealthChecking", waiting.Status.State, "a check names the new pod before the timeout: %s", framework.DescribeStep(waiting))
	assert.Contains(t, waiting.Status.Message, fmt.Sprintf("Deployment %s/%s rolling out: ", a.ns, fixtures.Workload("test")))
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", 2*time.Minute)
	assert.True(t, strings.HasPrefix(ps.Status.Message,
		"health alarm via resource (onHealthFailure=none): health check timeout after 40s; last result: waiting for resource: Deployment "), ps.Status.Message)
	assert.Regexp(t, newPodPullFailure, ps.Status.Message)
	assert.NotContains(t, ps.Status.Message, "ProgressDeadlineExceeded", "the timeout, not the progress deadline, ended the wait")
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
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
	p := a.resourcePipeline(nil)
	envSpec(t, p, "test").Bake = &bake
	a.apply(t, p)
	return a, e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
}

// waitBakeStarted waits for the first healthy check, which starts the window.
func waitBakeStarted(t *testing.T, e *framework.Env, a *app, bundle string, minutes int) *v1alpha1.PromotionStep {
	t.Helper()
	started := fmt.Sprintf("bake: 0m/%dm contiguous healthy via resource (~%dm remaining, resets=0)", minutes, minutes)
	return e.WaitStep(t, a.ns, pipelineName, bundle, "test", promoteTimeout, "the bake window to start",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.State == "HealthChecking" && ps.Status.BakeStartedAt != nil && ps.Status.Message == started, framework.DescribeStep(ps)
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
	at, c := verifiedCondition(t, ps)
	assert.Equal(t, "BakeComplete", c.Reason)
	assert.Equal(t, "contiguous soak 1m complete", c.Message)
	// Timestamps have second precision.
	assert.GreaterOrEqual(t, at.Sub(started), time.Minute-time.Second, "Verified %s, bake started %s", at, started)
	assert.Equal(t, 0, ps.Status.BakeResets)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// TestHealth_BakeResetOnAlarm checks bake.policy reset-on-alarm (the
// default): the pod turns unready during the window, and the first unhealthy
// check stops the window and counts one reset instead of failing. Later
// unhealthy checks wait for a healthy one without counting. Once the pod is
// ready again, the window restarts and a full window completes the bake.
// Covers BAKE-02.
func TestHealth_BakeResetOnAlarm(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a, bundle := bakePipeline(t, e, v1alpha1.BakeConfig{Minutes: 2})
	pods := map[string]string{"app.kubernetes.io/name": fixtures.Workload("test")}

	waitBakeStarted(t, e, a, bundle, 2)
	e.SetReadyz(t, a.ns, pods, false)
	unavailable := fmt.Sprintf("Deployment %s/%s: 0 of 1 updated replicas available (Available=False",
		a.ns, fixtures.Workload("test"))
	alarm := "bake: health alarm via resource — timer reset (resets=1, need 2m contiguous): " + unavailable
	waiting := "bake: waiting for a healthy check to restart the window (resets=1, unhealthy via resource): " + unavailable
	ps := e.WaitStep(t, a.ns, pipelineName, bundle, "test", time.Minute, "the alarm to reset the window",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.BakeResets >= 1, framework.DescribeStep(ps)
		})
	assert.Equal(t, 1, ps.Status.BakeResets)
	// The next check, 10s later, replaces the alarm's message.
	assert.True(t, strings.HasPrefix(ps.Status.Message, alarm) || strings.HasPrefix(ps.Status.Message, waiting), ps.Status.Message)
	assert.Nil(t, ps.Status.BakeStartedAt, "the alarm stops the window")
	e.WaitStep(t, a.ns, pipelineName, bundle, "test", time.Minute, "the next unhealthy check to wait",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return strings.HasPrefix(ps.Status.Message, waiting), framework.DescribeStep(ps)
		})
	framework.Consistently(t, 20*time.Second, "an alarm during the bake does not fail the step, and later unhealthy checks do not count",
		func(ctx context.Context) (bool, string) {
			ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
			if err != nil || ps == nil {
				return false, "step lookup failed"
			}
			return ps.Status.State == "HealthChecking" && ps.Status.BakeResets == 1, framework.DescribeStep(ps)
		})

	e.SetReadyz(t, a.ns, pods, true)
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", 4*time.Minute)
	assert.Equal(t, "bake complete: 2m contiguous healthy via resource (resets=1)", ps.Status.Message)
	assert.Equal(t, 1, ps.Status.BakeResets)
	at, _ := verifiedCondition(t, ps)
	assert.GreaterOrEqual(t, at.Sub(ps.Status.BakeStartedAt.Time), 2*time.Minute-time.Second,
		"a full window after the last reset")
}

// TestHealth_BakeFlappingEnds checks bake.policy reset-on-alarm on a release
// that keeps flapping (#1423): the pod turns unready during every window and
// ready again soon after, so no window lasts bake.minutes and each alarm
// re-arms health.timeout. The step used to stay HealthChecking forever. It
// now fails at the deadline: the first window's start
// (status.bakeFirstStartedAt) + bake.minutes + health.timeout.
// Covers BAKE-06.
func TestHealth_BakeFlappingEnds(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	p := a.resourcePipeline(nil)
	test := envSpec(t, p, "test")
	test.Bake = &v1alpha1.BakeConfig{Minutes: 1}
	test.Health.Timeout = "2m"
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	pods := map[string]string{"app.kubernetes.io/name": fixtures.Workload("test")}

	first := waitBakeStarted(t, e, a, bundle, 1).Status.BakeStartedAt.Time
	deadline := first.Add(3 * time.Minute)
	done := func(ps *v1alpha1.PromotionStep) bool { return ps.Status.State != "HealthChecking" }
	for resets := 1; ; resets++ {
		e.SetReadyz(t, a.ns, pods, false)
		ps := e.WaitStep(t, a.ns, pipelineName, bundle, "test", time.Minute, fmt.Sprintf("reset %d", resets),
			func(ps *v1alpha1.PromotionStep) (bool, string) {
				return done(ps) || ps.Status.BakeResets >= resets, framework.DescribeStep(ps)
			})
		if done(ps) {
			break
		}
		if assert.NotNil(t, ps.Status.BakeFirstStartedAt, "the first window's start is recorded") {
			assert.WithinDuration(t, first, ps.Status.BakeFirstStartedAt.Time, time.Second, "an alarm keeps the first window's start")
		}
		e.SetReadyz(t, a.ns, pods, true)
		ps = e.WaitStep(t, a.ns, pipelineName, bundle, "test", time.Minute, fmt.Sprintf("window %d", resets+1),
			func(ps *v1alpha1.PromotionStep) (bool, string) {
				return done(ps) || ps.Status.BakeStartedAt != nil, framework.DescribeStep(ps)
			})
		if done(ps) {
			break
		}
		require.Less(t, resets, 20, "the flapping release did not end: %s", framework.DescribeStep(ps))
	}
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", time.Minute)
	assert.True(t, strings.HasPrefix(ps.Status.Message,
		"health alarm via resource (onHealthFailure=none): bake: no 1m contiguous healthy window within 3m0s of the first healthy check "+
			"(bake.minutes + health.timeout; resets="), ps.Status.Message)
	failedAt := time.Now() // a few seconds after the step failed (WaitStep polls)
	assert.GreaterOrEqual(t, ps.Status.BakeResets, 2, "the release flapped more than once")
	assert.False(t, failedAt.Before(deadline.Add(-time.Second)), "failed at %s, before the deadline %s", failedAt, deadline)
	// The deadline ends the wait; health.timeout from the last alarm would be later.
	assert.True(t, failedAt.Before(deadline.Add(time.Minute)), "failed at %s, long after the deadline %s", failedAt, deadline)
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
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

// argoVerified is the Verified message of an argocd check that found the
// promoted revision (or, with no revision to find, the Bundle images).
const argoVerified = `health check passed via argocd: Healthy+Synced (opPhase="Succeeded")`

// short is a commit as the argocd adapter prints it.
func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// newHookedArgoApp is newArgoApp for one environment whose overlay also
// deploys an Argo CD hook Job named hook (fixtures.WithHook).
func newHookedArgoApp(t *testing.T, e *framework.Env, env, phase, script string) *app {
	t.Helper()
	ns := e.Namespace(t)
	files := fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{env}})
	fixtures.WithHook(files, env, "hook", phase, script)
	a := &app{e: e, ns: ns, envs: []string{env}, repo: e.Repo(t, ns, files)}
	e.ArgoApp(t, a.argoApp(env), a.repo, fixtures.Path(env), ns)
	e.WaitArgoApp(t, a.argoApp(env), syncTimeout)
	e.WaitDeploymentImage(t, ns, fixtures.Workload(env), fixtures.Image+":"+fixtures.V1, syncTimeout)
	return a
}

// onlyV1 is a hook script that passes only on fixtures.V1: the hook image
// follows the overlay's tag.
var onlyV1 = fmt.Sprintf(`test "$(./podinfo --version)" = %q`, fixtures.V1)

// TestHealth_ArgoVerified checks the argocd adapter's pass: the step is
// Verified only once the Application is Healthy, Synced and its last
// operation Succeeded on the commit the step pushed. Covers HEALTH-ARGO-01.
func TestHealth_ArgoVerified(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, argoVerified, ps.Status.Message)
	commit := ps.Status.Outputs["commitSHA"]
	require.NotEmpty(t, commit, "an auto promotion records the commit it pushed")
	app := a.argoApp("test")
	assert.Equal(t, commit, e.ArgoField(t, app, "status", "sync", "revision"), "Argo CD synced the pushed commit")
	assert.Equal(t, "Succeeded", e.ArgoField(t, app, "status", "operationState", "phase"))
	assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/kustomization.yaml"), "newTag: "+fixtures.V2)
	assert.Equal(t, imageV2, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// TestHealth_ArgoFailures checks the argocd adapter's two health failures,
// each in its own app: a Degraded Application (the new pods never pull, so
// the rollout passes its progress deadline) and a failed sync operation (a
// PostSync hook that fails on the new version while the Deployment runs it;
// Argo CD retries it once, so the operation is Failed within a minute).
// Neither is terminal: each counts failures until health.timeout fails the
// step. Covers HEALTH-ARGO-02.
func TestHealth_ArgoFailures(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	degraded := newArgoApp(t, e, "test")
	hooked := newHookedArgoApp(t, e, "test", "PostSync", onlyV1)
	e.SetArgoSyncRetry(t, hooked.argoApp("test"), 1)
	for a, timeout := range map[*app]string{degraded: "2m", hooked: "3m"} {
		p := a.pipeline(nil)
		envSpec(t, p, "test").Health.Timeout = timeout
		a.apply(t, p)
	}
	broken := e.CreateBundle(t, degraded.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
	failing := e.CreateBundle(t, hooked.ns, pipelineName, "--image", imageV2)

	cases := []struct {
		a       *app
		bundle  string
		state   string
		timeout string
	}{
		{degraded, broken, "health=Degraded, sync=Synced, opPhase=Succeeded", "2m0s"},
		{hooked, failing, "health=Healthy, sync=Synced, opPhase=Failed", "3m0s"},
	}
	for _, c := range cases {
		unhealthy := "unhealthy via argocd: " + c.state
		e.WaitStep(t, c.a.ns, pipelineName, c.bundle, "test", promoteTimeout, "an unhealthy check",
			func(ps *v1alpha1.PromotionStep) (bool, string) {
				return ps.Status.State == "HealthChecking" && ps.Status.Message == unhealthy && ps.Status.ConsecutiveHealthFailures >= 1, framework.DescribeStep(ps)
			})
	}
	for _, c := range cases {
		ps := e.WaitStepState(t, c.a.ns, pipelineName, c.bundle, "test", "Failed", 4*time.Minute)
		assert.Equal(t, "health alarm via argocd (onHealthFailure=none): health check timeout after "+c.timeout+
			"; last result: unhealthy via argocd: "+c.state, ps.Status.Message)
		assert.GreaterOrEqual(t, ps.Status.ConsecutiveHealthFailures, 2)
		e.WaitBundlePhase(t, c.a.ns, c.bundle, "Failed", time.Minute)
	}
	assert.Equal(t, "Degraded", e.ArgoField(t, degraded.argoApp("test"), "status", "health", "status"))
	assert.Equal(t, "Failed", e.ArgoField(t, hooked.argoApp("test"), "status", "operationState", "phase"))
	assert.Equal(t, imageV2, e.DeploymentImage(t, hooked.ns, fixtures.Workload("test")),
		"the Deployment runs the new version; only the PostSync hook failed")
}

// TestHealth_ArgoIgnoresEarlierFailure checks B52: a failure left by the
// version before the promotion does not count against it. A PostSync hook
// fails on fixtures.V1 once the test creates a marker, and a sync of V1
// fails. With auto-sync off, the step for V2 waits, with no health failure,
// while Argo CD still shows that Failed operation: first Synced on the old
// commit, then OutOfSync on the pushed one. With auto-sync back on, Argo CD
// syncs the pushed commit, the hook passes on V2, and the step is Verified.
// Covers HEALTH-ARGO-06.
func TestHealth_ArgoIgnoresEarlierFailure(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	const marker = "fail-on-v1"
	script := fmt.Sprintf(`! %s || test "$(./podinfo --version)" != %q`, fixtures.MarkerExists(ns, marker), fixtures.V1)
	files := fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}})
	fixtures.WithHook(files, "test", "hook", "PostSync", script)
	a := &app{e: e, ns: ns, envs: []string{"test"}, repo: e.Repo(t, ns, files)}
	app := a.argoApp("test")
	e.ArgoApp(t, app, a.repo, fixtures.Path("test"), ns)
	old := e.WaitArgoOperation(t, app, "Succeeded", syncTimeout)
	e.WaitDeploymentImage(t, ns, fixtures.Workload("test"), fixtures.Image+":"+fixtures.V1, syncTimeout)

	e.CreateMarker(t, ns, marker)
	e.SetArgoAutoSync(t, app, false)
	e.SyncArgoApp(t, app)
	require.Equal(t, old, e.WaitArgoOperation(t, app, "Failed", 2*time.Minute), "the failed sync ran on the old commit")
	p := a.pipeline(nil)
	envSpec(t, p, "test").Health.Timeout = "5m"
	a.apply(t, p)

	bundle := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
	ps := e.WaitStep(t, ns, pipelineName, bundle, "test", promoteTimeout, "the pushed commit",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.State == "HealthChecking" && ps.Status.Outputs["commitSHA"] != "", framework.DescribeStep(ps)
		})
	commit := ps.Status.Outputs["commitSHA"]
	// Argo CD polls git every 10s; until then it is Synced on the old commit.
	// Waiting results do not reset the failure count, so the count checked
	// below also covers the checks before it.
	waiting := fmt.Sprintf("waiting for argocd: health=Healthy, sync=OutOfSync, opPhase=Failed, revision=%s not synced yet, "+
		"ignoring the operation on %s", short(commit), short(old))
	e.WaitStep(t, ns, pipelineName, bundle, "test", time.Minute, "the check to see the pushed commit OutOfSync",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.Message == waiting, framework.DescribeStep(ps)
		})
	assert.Equal(t, commit, e.ArgoField(t, app, "status", "sync", "revision"), "Argo CD fetched the pushed commit")
	framework.Consistently(t, 20*time.Second, "a failure on the old commit does not count", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, ns, pipelineName, bundle, "test")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return ps.Status.State == "HealthChecking" && ps.Status.Message == waiting &&
			ps.Status.ConsecutiveHealthFailures == 0, framework.DescribeStep(ps)
	})
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, ns, fixtures.Workload("test")))

	e.SetArgoAutoSync(t, app, true)
	ps = e.WaitStepState(t, ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, argoVerified, ps.Status.Message)
	assert.Equal(t, 0, ps.Status.ConsecutiveHealthFailures)
	assert.Equal(t, commit, e.ArgoField(t, app, "status", "operationState", "syncResult", "revision"))
	assert.Equal(t, imageV2, e.DeploymentImage(t, ns, fixtures.Workload("test")))
	e.WaitBundlePhase(t, ns, bundle, "Verified", time.Minute)
}

// TestHealth_ArgoIgnoresEarlierDegraded checks B52 for Degraded health, as
// in a fix-forward after a broken release: the app runs fixtures.BrokenTag,
// so Argo CD reports it Degraded once the rollout passes its progress
// deadline. The step for V2 waits, with no health failure, while Argo CD
// (auto-sync off) is OutOfSync on the pushed commit, and again while the
// sync of that commit runs a PreSync hook that waits for a marker, the
// Degraded Deployment still on BrokenTag. Once the test creates the marker,
// Argo CD applies V2 and the step is Verified. Covers HEALTH-ARGO-06.
func TestHealth_ArgoIgnoresEarlierDegraded(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	const release = "release-presync"
	files := fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}, Tag: fixtures.BrokenTag})
	a := &app{e: e, ns: ns, envs: []string{"test"}, repo: e.Repo(t, ns, files)}
	app := a.argoApp("test")
	e.ArgoApp(t, app, a.repo, fixtures.Path("test"), ns)
	old := e.WaitArgoOperation(t, app, "Succeeded", syncTimeout)
	e.WaitArgoHealth(t, app, "Degraded", 3*time.Minute)

	// The hook's image follows the overlay's tag, which Argo CD cannot pull
	// on BrokenTag, so the hook is added with auto-sync off and first runs
	// on the pushed commit.
	e.SetArgoAutoSync(t, app, false)
	fixtures.WithHook(files, "test", "hook", "PreSync", fmt.Sprintf("until %s; do sleep 1; done", fixtures.MarkerExists(ns, release)))
	kust := fixtures.Path("test") + "/kustomization.yaml"
	hook := fixtures.Path("test") + "/hook.yaml"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	_, err := gitserver.CommitFiles(ctx, e.Git, a.repo, a.repo.Branch, "", "Add a PreSync hook",
		map[string][]byte{kust: files[kust], hook: files[hook]})
	cancel()
	require.NoError(t, err)
	p := a.pipeline(nil)
	envSpec(t, p, "test").Health.Timeout = "5m"
	a.apply(t, p)

	bundle := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
	ps := e.WaitStep(t, ns, pipelineName, bundle, "test", promoteTimeout, "the pushed commit",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.State == "HealthChecking" && ps.Status.Outputs["commitSHA"] != "", framework.DescribeStep(ps)
		})
	commit := ps.Status.Outputs["commitSHA"]
	// Waiting results do not reset the failure count, so each count checked
	// below also covers the checks before it.
	noFailure := func(what, message string) {
		t.Helper()
		e.WaitStep(t, ns, pipelineName, bundle, "test", time.Minute, what,
			func(ps *v1alpha1.PromotionStep) (bool, string) {
				return ps.Status.Message == message, framework.DescribeStep(ps)
			})
		framework.Consistently(t, 20*time.Second, what+" is not a health failure", func(ctx context.Context) (bool, string) {
			ps, _, err := e.Step(ctx, ns, pipelineName, bundle, "test")
			if err != nil || ps == nil {
				return false, "step lookup failed"
			}
			return ps.Status.State == "HealthChecking" && ps.Status.Message == message &&
				ps.Status.ConsecutiveHealthFailures == 0, framework.DescribeStep(ps)
		})
		assert.Equal(t, fixtures.Image+":"+fixtures.BrokenTag, e.DeploymentImage(t, ns, fixtures.Workload("test")), what)
	}
	noFailure("the Degraded Application OutOfSync on the pushed commit", fmt.Sprintf(
		"waiting for argocd: health=Degraded, sync=OutOfSync, opPhase=Succeeded, revision=%s not synced yet", short(commit)))
	assert.Equal(t, old, e.ArgoField(t, app, "status", "operationState", "syncResult", "revision"),
		"the last operation ran on the BrokenTag commit")

	e.SetArgoAutoSync(t, app, true)
	noFailure("the PreSync hook of the pushed commit", fmt.Sprintf(
		"waiting for argocd: health=Degraded, sync=OutOfSync, opPhase=Running, revision=%s not synced yet", short(commit)))
	assert.Equal(t, commit, e.ArgoField(t, app, "status", "operationState", "operation", "sync", "revision"),
		"the running operation is the pushed commit's")

	e.CreateMarker(t, ns, release)
	ps = e.WaitStepState(t, ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, argoVerified, ps.Status.Message)
	assert.Equal(t, commit, e.ArgoField(t, app, "status", "operationState", "syncResult", "revision"))
	assert.Equal(t, imageV2, e.DeploymentImage(t, ns, fixtures.Workload("test")))
	e.WaitBundlePhase(t, ns, bundle, "Verified", time.Minute)
}

// TestHealth_ArgoWaitsForRevision checks that the argocd adapter does not
// pass an Application that is Healthy and Synced on an older commit (pinned
// to it), nor one whose sync is still running (a PreSync hook that, on the
// new version, waits for a marker the test creates). Both are waiting, not
// failures. Covers HEALTH-ARGO-03.
func TestHealth_ArgoWaitsForRevision(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	const release = "release-presync"
	files := fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}})
	fixtures.WithHook(files, "test", "hook", "PreSync",
		fmt.Sprintf("%s || until %s; do sleep 1; done", onlyV1, fixtures.MarkerExists(ns, release)))
	a := &app{e: e, ns: ns, envs: []string{"test"}, repo: e.Repo(t, ns, files)}
	app := a.argoApp("test")
	e.ArgoApp(t, app, a.repo, fixtures.Path("test"), ns)
	e.WaitArgoApp(t, app, syncTimeout)
	e.WaitDeploymentImage(t, ns, fixtures.Workload("test"), fixtures.Image+":"+fixtures.V1, syncTimeout)
	old := e.ArgoField(t, app, "status", "sync", "revision")
	require.NotEmpty(t, old)
	e.SetArgoTargetRevision(t, app, old)
	p := a.pipeline(nil)
	envSpec(t, p, "test").Health.Timeout = "5m"
	a.apply(t, p)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStep(t, a.ns, pipelineName, bundle, "test", promoteTimeout, "the pushed commit",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.State == "HealthChecking" && ps.Status.Outputs["commitSHA"] != "", framework.DescribeStep(ps)
		})
	commit := ps.Status.Outputs["commitSHA"]
	waiting := fmt.Sprintf("waiting for argocd: health=Healthy, sync=Synced, opPhase=Succeeded, revision=%s, waiting for %s",
		short(old), short(commit))
	e.WaitStep(t, a.ns, pipelineName, bundle, "test", time.Minute, "the older revision to hold the step",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.Message == waiting, framework.DescribeStep(ps)
		})
	framework.Consistently(t, 20*time.Second, "an Application on an older commit does not pass", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return ps.Status.State == "HealthChecking" && ps.Status.Message == waiting &&
			ps.Status.ConsecutiveHealthFailures == 0, framework.DescribeStep(ps)
	})
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))

	e.SetArgoTargetRevision(t, app, a.repo.Branch)
	ps = e.WaitStep(t, a.ns, pipelineName, bundle, "test", time.Minute, "a running sync to hold the step",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.State == "HealthChecking" && strings.Contains(ps.Status.Message, "opPhase=Running"), framework.DescribeStep(ps)
		})
	assert.True(t, strings.HasPrefix(ps.Status.Message, "waiting for argocd: "), ps.Status.Message)
	framework.Consistently(t, 15*time.Second, "a running sync does not pass or fail", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return ps.Status.State == "HealthChecking" && strings.Contains(ps.Status.Message, "opPhase=Running") &&
			ps.Status.ConsecutiveHealthFailures == 0, framework.DescribeStep(ps)
	})
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("test")),
		"the PreSync hook still runs")

	e.CreateMarker(t, a.ns, release)
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, argoVerified, ps.Status.Message)
	assert.Equal(t, commit, e.ArgoField(t, app, "status", "sync", "revision"))
	assert.Equal(t, imageV2, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
}

// TestHealth_ArgoSharedBranch checks the shared-branch fallback: two
// Pipelines push to one branch, and Argo CD (auto-sync off in test) never
// syncs test's own commit, only the later one from the other Pipeline. The
// adapter accepts that revision because the Application runs the Bundle
// image, and says so. Covers HEALTH-ARGO-04.
func TestHealth_ArgoSharedBranch(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "uat")
	both := a.pipeline(nil)
	test, uat := both.DeepCopy(), both.DeepCopy()
	test.Spec.Environments = []v1alpha1.EnvironmentSpec{*envSpec(t, both, "test")}
	uat.Name = pipelineName + "-uat"
	uat.Spec.Environments = []v1alpha1.EnvironmentSpec{*envSpec(t, both, "uat")}
	a.apply(t, test)
	a.apply(t, uat)
	e.SetArgoAutoSync(t, a.argoApp("test"), false)

	first := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStep(t, a.ns, pipelineName, first, "test", promoteTimeout, "test's commit",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.State == "HealthChecking" && ps.Status.Outputs["commitSHA"] != "", framework.DescribeStep(ps)
		})
	own := ps.Status.Outputs["commitSHA"]
	second := e.CreateBundle(t, a.ns, uat.Name, "--image", imageV2)
	later := e.WaitStepState(t, a.ns, uat.Name, second, "uat", "Verified", promoteTimeout).Status.Outputs["commitSHA"]
	require.NotEmpty(t, later)
	require.NotEqual(t, own, later)
	framework.Consistently(t, 15*time.Second, "test waits while Argo CD does not sync it", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, first, "test")
		if err != nil || ps == nil {
			return false, fmt.Sprintf("step lookup: %v", err)
		}
		return ps.Status.State == "HealthChecking" && ps.Status.ConsecutiveHealthFailures == 0, framework.DescribeStep(ps)
	})
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))

	e.SetArgoAutoSync(t, a.argoApp("test"), true)
	ps = e.WaitStepState(t, a.ns, pipelineName, first, "test", "Verified", promoteTimeout)
	assert.Equal(t, fmt.Sprintf("%s (synced revision %s is not %s, but the Application runs the Bundle images)",
		argoVerified, short(later), short(own)), ps.Status.Message)
	assert.Equal(t, later, e.ArgoField(t, a.argoApp("test"), "status", "sync", "revision"))
	assert.Equal(t, imageV2, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
}

// TestHealth_ArgoMergeCommit checks that with approval: pr-review the argocd
// adapter waits for the PR's merge commit: the step records it as
// outputs.mergeCommitSHA (it pushed nothing to the branch itself), waits
// while Argo CD is pinned to the commit before the merge, and passes once
// Argo CD syncs the merge commit. Covers HEALTH-ARGO-05.
func TestHealth_ArgoMergeCommit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	app := a.argoApp("test")
	old := e.ArgoField(t, app, "status", "sync", "revision")
	require.NotEmpty(t, old)
	e.SetArgoTargetRevision(t, app, old)
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "the promotion PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	require.NoError(t, e.Git.MergePR(context.Background(), a.repo, pr.Number))

	ps := e.WaitStep(t, a.ns, pipelineName, bundle, "test", 2*time.Minute, "the merge commit",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.State == "HealthChecking" && ps.Status.Outputs["mergeCommitSHA"] != "", framework.DescribeStep(ps)
		})
	merge := ps.Status.Outputs["mergeCommitSHA"]
	assert.Empty(t, ps.Status.Outputs["commitSHA"], "a pr-review step pushes only its PR branch")
	waiting := fmt.Sprintf("waiting for argocd: health=Healthy, sync=Synced, opPhase=Succeeded, revision=%s, waiting for %s",
		short(old), short(merge))
	e.WaitStep(t, a.ns, pipelineName, bundle, "test", time.Minute, "the merge commit to be awaited",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.Message == waiting, framework.DescribeStep(ps)
		})

	e.SetArgoTargetRevision(t, app, a.repo.Branch)
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, argoVerified, ps.Status.Message)
	assert.Equal(t, merge, e.ArgoField(t, app, "status", "sync", "revision"), "the merge commit is the branch head Argo CD synced")
	assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/kustomization.yaml"), "newTag: "+fixtures.V2)
	assert.Equal(t, imageV2, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
}

// argoImageKey is fixtures.HelmRepo's image tag as an update.argocd.imageKey.
// The Application's valuesObject overrides the chart's values-e2e.yaml.
var argoImageKey = strings.TrimPrefix(fixtures.HelmImagePath, ".")

// argoStrategyApp is a test namespace with a fixtures.HelmRepo for test and
// an auto-syncing Argo CD Application that renders test's chart with
// fixtures.HelmValuesFile and, unless nil, spec.source.helm.valuesObject
// values, synced on fixtures.V1. It returns the namespace, the repo and the
// Application name.
func argoStrategyApp(t *testing.T, e *framework.Env, values map[string]interface{}) (string, gitserver.Repo, string) {
	t.Helper()
	ns := e.Namespace(t)
	repo := e.Repo(t, ns, fixtures.HelmRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}}))
	app := ns + "-test"
	// Helm release names are at most 53 characters; the Application name
	// (namespace-env), the default, can be longer.
	helm := map[string]interface{}{"releaseName": "podinfo", "valueFiles": []interface{}{fixtures.HelmValuesFile}}
	if values != nil {
		helm["valuesObject"] = values
	}
	e.ArgoAppSpec(t, app, map[string]interface{}{
		"project": "default",
		"source": map[string]interface{}{
			"repoURL": repo.CloneURL, "targetRevision": repo.Branch, "path": fixtures.Path("test"), "helm": helm,
		},
		"destination": map[string]interface{}{"server": "https://kubernetes.default.svc", "namespace": ns},
		"syncPolicy": map[string]interface{}{
			"automated": map[string]interface{}{"prune": true, "selfHeal": true},
		},
	}, false)
	e.WaitArgoApp(t, app, syncTimeout)
	e.WaitDeploymentImage(t, ns, fixtures.Workload("test"), imageV1, syncTimeout)
	return ns, repo, app
}

// argoStrategyEnv is an environment that promotes by patching Argo CD
// Application app (update.strategy argocd) and checks it with argocd health.
func argoStrategyEnv(name, app, imageKey string) v1alpha1.EnvironmentSpec {
	return v1alpha1.EnvironmentSpec{
		Name:     name,
		Approval: "auto",
		Update: v1alpha1.UpdateConfig{Strategy: "argocd", ArgoCD: &v1alpha1.ArgoCDUpdateConfig{
			Application: app, Namespace: framework.ArgoCDNamespace, ImageKey: imageKey}},
		Health: v1alpha1.HealthConfig{Type: "argocd", Timeout: healthTimeout,
			ArgoCD: &v1alpha1.HealthTargetRef{Name: app, Namespace: framework.ArgoCDNamespace}},
	}
}

// argoStrategyPipeline is a Pipeline over repo with envs.
func argoStrategyPipeline(ns string, repo gitserver.Repo, envs ...v1alpha1.EnvironmentSpec) *v1alpha1.Pipeline {
	return &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: pipelineName, Namespace: ns},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: repo.CloneURL, Branch: repo.Branch,
				SecretRef: &v1alpha1.SecretRef{Name: framework.GitSecretName}},
			Environments: envs,
		},
	}
}

// valuesKey reads a dot path of the Application's spec.source.helm.valuesObject.
func valuesKey(t *testing.T, e *framework.Env, app, key string) string {
	t.Helper()
	return e.ArgoField(t, app, append([]string{"spec", "source", "helm", "valuesObject"}, strings.Split(key, ".")...)...)
}

// assertNoGitChange checks that a promotion changed nothing in git: no PR,
// the chart's values files as argoStrategyApp wrote them, and Argo CD still
// on commit.
func assertNoGitChange(t *testing.T, e *framework.Env, repo gitserver.Repo, app, commit string) {
	t.Helper()
	prs, err := e.Git.PullRequests(context.Background(), repo)
	require.NoError(t, err)
	assert.Empty(t, prs, "the argocd strategy opens no PR")
	files := fixtures.HelmRepo(fixtures.App{Envs: []string{"test"}})
	for _, f := range []string{"values.yaml", fixtures.HelmValuesFile} {
		f = fixtures.Path("test") + "/" + f
		assert.Equal(t, string(files[f]), e.ReadFile(t, repo, repo.Branch, f), f)
	}
	assert.Equal(t, commit, e.ArgoField(t, app, "status", "sync", "revision"), "no commit on the branch Argo CD tracks")
}

// TestHealth_ArgoStrategyPatchesApplication checks update.strategy argocd:
// the step patches imageKey in the Application's
// spec.source.helm.valuesObject, Argo CD rolls the Deployment out, and argocd
// health (no commit to find, so it checks the Bundle image) verifies it. No
// commit or PR is made. Covers ARGOSTRAT-01.
func TestHealth_ArgoStrategyPatchesApplication(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns, repo, app := argoStrategyApp(t, e, map[string]interface{}{
		"app": map[string]interface{}{"image": map[string]interface{}{"tag": fixtures.V1}}})
	e.GrantArgoPatch(t, ns, app)
	commit := e.ArgoField(t, app, "status", "sync", "revision")
	require.NoError(t, e.Client.Create(context.Background(), argoStrategyPipeline(ns, repo, argoStrategyEnv("test", app, argoImageKey))))

	bundle := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
	ps := e.WaitStepState(t, ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, argoVerified, ps.Status.Message)
	assert.Equal(t, map[string]string{"argocdApplication": app, "argocdNamespace": framework.ArgoCDNamespace,
		"imageKey": argoImageKey, "imageTag": fixtures.V2}, ps.Status.Outputs, "no commitSHA: nothing was pushed")
	require.Len(t, ps.Status.Steps, 2)
	assert.Equal(t, "argocd-set-image", ps.Status.Steps[0].Name)
	assert.Equal(t, "health-check", ps.Status.Steps[1].Name)
	assert.Equal(t, fixtures.V2, valuesKey(t, e, app, argoImageKey))
	assert.Equal(t, imageV2, e.DeploymentImage(t, ns, fixtures.Workload("test")))
	assertNoGitChange(t, e, repo, app, commit)
	e.WaitBundlePhase(t, ns, bundle, "Verified", time.Minute)
}

// valuesObject is the Application's spec.source.helm.valuesObject, nil when
// unset.
func valuesObject(t *testing.T, e *framework.Env, app string) map[string]interface{} {
	t.Helper()
	v, _, err := unstructured.NestedMap(e.GetArgoApp(t, app).Object, "spec", "source", "helm", "valuesObject")
	require.NoError(t, err)
	return v
}

// TestHealth_ArgoStrategyImageKey checks update.argocd.imageKey and
// multi-image Bundles: the Application has no valuesObject (its values come
// from the chart's values file), the patch creates the maps of the nested
// key and writes only that key, and of three Bundle images the step uses the
// first with a tag (the first has only a digest). Covers ARGOSTRAT-02.
func TestHealth_ArgoStrategyImageKey(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns, repo, app := argoStrategyApp(t, e, nil)
	e.GrantArgoPatch(t, ns, app)
	commit := e.ArgoField(t, app, "status", "sync", "revision")
	require.Nil(t, valuesObject(t, e, app))
	require.NoError(t, e.Client.Create(context.Background(),
		argoStrategyPipeline(ns, repo, argoStrategyEnv("test", app, argoImageKey))))

	bundle := e.CreateBundle(t, ns, pipelineName,
		"--image", "ghcr.io/kardinal-e2e/sidecar@sha256:"+strings.Repeat("a", 64),
		"--image", imageV2,
		"--image", "ghcr.io/kardinal-e2e/other:"+fixtures.V3)
	ps := e.WaitStepState(t, ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, argoVerified, ps.Status.Message)
	assert.Equal(t, argoImageKey, ps.Status.Outputs["imageKey"])
	assert.Equal(t, fixtures.V2, ps.Status.Outputs["imageTag"], "the first Bundle image with a tag")
	assert.Equal(t, map[string]interface{}{"app": map[string]interface{}{"image": map[string]interface{}{"tag": fixtures.V2}}},
		valuesObject(t, e, app), "only imageKey is patched")
	assert.Equal(t, imageV2, e.DeploymentImage(t, ns, fixtures.Workload("test")),
		"the repository still comes from the chart's values file")
	assertNoGitChange(t, e, repo, app, commit)
}

// TestHealth_ArgoStrategyRejectsConfigBundles checks that a config or mixed
// Bundle fails at graph build when an environment it promotes uses the
// argocd strategy, even a later one: test (kustomize) gets no PromotionStep,
// and git, the Deployment and the Application stay as they were.
// Covers ARGOSTRAT-03.
func TestHealth_ArgoStrategyRejectsConfigBundles(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	prodApp := a.ns + "-prod-helm"
	p := a.pipeline(nil)
	p.Spec.Environments = append(p.Spec.Environments, argoStrategyEnv("prod", prodApp, ""))
	a.apply(t, p)
	commit := e.ArgoField(t, a.argoApp("test"), "status", "sync", "revision")

	for _, typ := range []string{"mixed", "config"} {
		args := []string{"--type", typ, "--config-commit", commit}
		if typ == "mixed" {
			args = append(args, "--image", imageV2)
		}
		bundle := e.CreateBundle(t, a.ns, pipelineName, args...)
		b := e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
		c := meta.FindStatusCondition(b.Status.Conditions, "InvalidSpec")
		require.NotNil(t, c, "%s Bundle: InvalidSpec condition: %v", typ, b.Status.Conditions)
		assert.Equal(t, metav1.ConditionTrue, c.Status)
		assert.Equal(t, "GraphBuildFailed", c.Reason)
		// The layer prefix is "translator.Translate: build: ", doubled
		// ("build: build: ") until B47, so the check is on its two ends.
		assert.True(t, strings.HasPrefix(c.Message, "translator.Translate: build: "), c.Message)
		assert.True(t, strings.HasSuffix(c.Message, fmt.Sprintf(`environment "prod" uses update.strategy argocd, which does not support %s Bundles: `+
			"it sets only the image in the Argo CD Application and would skip the config change; use a git-based strategy "+
			"(kustomize or helm) for that environment, or skip it with intent.skipEnvironments — fix the Pipeline, Bundle "+
			"or PolicyGate it names; a change to the Pipeline or its PolicyGates retries this Bundle", typ)), c.Message)
		var steps v1alpha1.PromotionStepList
		require.NoError(t, e.Client.List(context.Background(), &steps, client.InNamespace(a.ns)))
		assert.Empty(t, steps.Items, "%s Bundle: no environment started", typ)
	}
	assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/kustomization.yaml"), "newTag: "+fixtures.V1)
	assert.Equal(t, commit, e.ArgoField(t, a.argoApp("test"), "status", "sync", "revision"), "no commit")
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
	prs, err := e.Git.PullRequests(context.Background(), a.repo)
	require.NoError(t, err)
	assert.Empty(t, prs)
}

// TestHealth_ArgoStrategyNeedsPatchRBAC checks the argocd strategy's RBAC.
// The chart installed with defaults grants no patch on Applications, so the
// step fails with the API server's forbidden error after its retries and
// nothing changes. helm template shows rbac.argocdApplicationsWrite=true
// adds patch to the chart's Applications rule. The test does not upgrade the
// installed chart: it binds the rendered rule to the controller in a test
// Role in the Application namespace, for this Application only, and the next
// Bundle promotes. CHART-ARGOCDWRITE-01 is thus checked on the rules helm
// template renders, not on an installed release's RBAC. Covers ARGOSTRAT-04,
// CHART-ARGOCDWRITE-01.
func TestHealth_ArgoStrategyNeedsPatchRBAC(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns, repo, app := argoStrategyApp(t, e, map[string]interface{}{
		"app": map[string]interface{}{"image": map[string]interface{}{"tag": fixtures.V1}}})
	require.False(t, e.ControllerCan(t, "patch", app), "the chart's default RBAC grants no patch on Applications")
	require.True(t, e.ControllerCan(t, "get", app))
	require.NoError(t, e.Client.Create(context.Background(), argoStrategyPipeline(ns, repo, argoStrategyEnv("test", app, argoImageKey))))

	denied := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
	// The message is "step argocd-set-image: <what failed>: <API error>".
	// What failed reads "argocd-set-image: patch Application" until B47 and
	// "patch Application <namespace>/<name>" after, so the check is on the
	// step, the action and the API server's error.
	forbidden := fmt.Sprintf(`applications.argoproj.io %q is forbidden: `+
		`User "system:serviceaccount:%s:%s" cannot patch resource "applications" in API group "argoproj.io" in the namespace %q`,
		app, framework.ControllerNamespace, framework.ControllerServiceAccount, framework.ArgoCDNamespace)
	isForbidden := func(msg, prefix, suffix string) bool {
		return strings.HasPrefix(msg, prefix+"step argocd-set-image: ") && strings.Contains(msg, "patch Application") &&
			strings.HasSuffix(msg, forbidden+suffix)
	}
	e.WaitStep(t, ns, pipelineName, denied, "test", promoteTimeout, "the forbidden patch to be retried",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.State == "Promoting" && isForbidden(ps.Status.Message, "retrying in 10s (1/5) after error: ", ""),
				framework.DescribeStep(ps)
		})
	// Retries back off 10s, 20s, 40s, 80s and 120s.
	ps := e.WaitStepState(t, ns, pipelineName, denied, "test", "Failed", 7*time.Minute)
	assert.True(t, isForbidden(ps.Status.Message, "", " (gave up after 5 retries)"), ps.Status.Message)
	e.WaitBundlePhase(t, ns, denied, "Failed", time.Minute)
	assert.Equal(t, fixtures.V1, valuesKey(t, e, app, argoImageKey))
	assert.Equal(t, imageV1, e.DeploymentImage(t, ns, fixtures.Workload("test")))

	verbs := func(rules []rbacv1.PolicyRule) [][]string {
		var out [][]string
		for _, r := range rules {
			out = append(out, r.Verbs)
		}
		return out
	}
	assert.Equal(t, [][]string{{"get", "list", "watch"}}, verbs(framework.ChartApplicationRules(t)))
	write := framework.ChartApplicationRules(t, "rbac.argocdApplicationsWrite=true")
	require.Equal(t, [][]string{{"get", "list", "watch", "patch"}}, verbs(write))
	// Bound for this test's Application only: the tests running in parallel
	// keep the chart's default RBAC on theirs.
	for i := range write {
		write[i].ResourceNames = []string{app}
	}
	e.BindArgoRules(t, ns, write)
	require.True(t, e.ControllerCan(t, "patch", app))
	require.False(t, e.ControllerCan(t, "patch", ""), "patch is granted on %s only", app)

	bundle := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
	ps = e.WaitStepState(t, ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, argoVerified, ps.Status.Message)
	assert.Equal(t, fixtures.V2, valuesKey(t, e, app, argoImageKey))
	assert.Equal(t, imageV2, e.DeploymentImage(t, ns, fixtures.Workload("test")))
}

// appSetGVR is Argo CD's ApplicationSet.
var appSetGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}

// TestHealth_MultiTenantExample runs examples/multi-tenant as a platform team
// would: a platform repo with the example's team-pipeline chart and one
// folder per team, and the example's root ApplicationSet pointed at it. Argo
// CD renders each team's Pipeline into the team's namespace, with the team's
// GitOps repo and git Secret, and each team's Bundle promotes through it. The
// chart sets no health, so the default resource health reads the Deployment
// named after the Pipeline in the namespace named after the environment: each
// team names its app podinfo-<namespace> and its one environment after its
// namespace.
//
// Covers EX-TENANT-01.
func TestHealth_MultiTenantExample(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	example := filepath.Join("..", "..", "..", "examples", "multi-tenant")
	const dir = "examples/multi-tenant/"
	files := map[string][]byte{}
	for _, f := range []string{"chart/Chart.yaml", "chart/values.yaml", "chart/templates/pipeline.yaml"} {
		raw, err := os.ReadFile(filepath.Join(example, f))
		require.NoError(t, err)
		files[dir+f] = raw
	}
	type team struct {
		ns   string
		repo gitserver.Repo
	}
	var teams []team
	for range 2 {
		ns := e.Namespace(t)
		repo := e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{ns}}))
		e.ArgoApp(t, ns, repo, fixtures.Path(ns), ns)
		files[dir+"teams/"+ns+"/pipeline-values.yaml"] = []byte(fmt.Sprintf(
			"appName: %s\ngitRepo: %s\ngitBranch: %s\ngitSecretName: %s\nenvironments:\n  - name: %s\n",
			fixtures.Workload(ns), repo.CloneURL, repo.Branch, framework.GitSecretName, ns))
		teams = append(teams, team{ns: ns, repo: repo})
	}
	platform := e.Repo(t, teams[0].ns+"-platform", files)

	raw, err := os.ReadFile(filepath.Join(example, "root-appset.yaml"))
	require.NoError(t, err)
	name := teams[0].ns + "-teams"
	set := strings.NewReplacer(
		"https://github.com/pnz1990/kardinal-promoter", platform.CloneURL,
		"revision: main", "revision: "+platform.Branch,
		"name: team-pipelines", "name: "+name,
	).Replace(string(raw))
	var obj unstructured.Unstructured
	require.NoError(t, yaml.Unmarshal([]byte(set), &obj.Object))
	obj.SetLabels(map[string]string{"kardinal.io/e2e": "true"})
	ctx := context.Background()
	appSets := e.Dynamic.Resource(appSetGVR).Namespace(framework.ArgoCDNamespace)
	_, err = appSets.Create(ctx, &obj, metav1.CreateOptions{})
	require.NoError(t, err, "create ApplicationSet %s", name)
	t.Cleanup(func() {
		if err := appSets.Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete ApplicationSet %s: %v", name, err)
		}
	})

	bundles := map[string]string{}
	for _, tm := range teams {
		e.WaitArgoApp(t, tm.ns+"-pipeline", syncTimeout)
		pipeline := fixtures.Workload(tm.ns)
		var p v1alpha1.Pipeline
		require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: tm.ns, Name: pipeline}, &p))
		assert.Equal(t, map[string]string{"kardinal.io/managed-by": "appset", "kardinal.io/team": tm.ns},
			map[string]string{"kardinal.io/managed-by": p.Labels["kardinal.io/managed-by"], "kardinal.io/team": p.Labels["kardinal.io/team"]})
		assert.Equal(t, v1alpha1.PipelineGit{URL: tm.repo.CloneURL, Branch: tm.repo.Branch, Layout: "directory",
			SecretRef: &v1alpha1.SecretRef{Name: framework.GitSecretName}}, p.Spec.Git, "the team's repo and Secret")
		assert.Equal(t, []string{framework.PolicyNamespace, tm.ns}, p.Spec.PolicyNamespaces)
		assert.Equal(t, 10, p.Spec.HistoryLimit)
		require.Len(t, p.Spec.Environments, 1)
		env := p.Spec.Environments[0]
		assert.Equal(t, []string{tm.ns, fixtures.Path(tm.ns), "auto"}, []string{env.Name, env.Path, env.Approval})

		e.WaitArgoApp(t, tm.ns, syncTimeout)
		e.WaitDeploymentImage(t, tm.ns, pipeline, fixtures.Image+":"+fixtures.V1, syncTimeout)
		bundles[tm.ns] = e.CreateBundle(t, tm.ns, pipeline, "--image", imageV2)
	}
	for _, tm := range teams {
		pipeline := fixtures.Workload(tm.ns)
		ps := e.WaitStepState(t, tm.ns, pipeline, bundles[tm.ns], tm.ns, "Verified", promoteTimeout)
		assert.Equal(t, "health check passed via resource: Available=True: Deployment has minimum availability., "+
			"1/1 replicas updated and available", ps.Status.Message)
		e.WaitBundlePhase(t, tm.ns, bundles[tm.ns], "Verified", time.Minute)
		assert.Contains(t, e.ReadFile(t, tm.repo, tm.repo.Branch, fixtures.Path(tm.ns)+"/kustomization.yaml"), "newTag: "+fixtures.V2)
		e.WaitDeploymentImage(t, tm.ns, pipeline, imageV2, syncTimeout)
	}
}
