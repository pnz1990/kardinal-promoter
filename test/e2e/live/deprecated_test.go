//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The deprecated tests check that each deprecated field does what the
// changelog's Deprecated section says: accepted and ignored, or rejected with
// a message that says what to do instead. Each test runs its own
// namespace-mode release, so they run in the chart suite.

// deprecatedApp is an app with Argo CD syncing its workloads and a
// namespace-mode controller release watching its namespace.
func deprecatedApp(t *testing.T, e *framework.Env, envs ...string) *app {
	t.Helper()
	a := newArgoApp(t, e, envs...)
	r := e.InstallChart(t, releaseName(a.ns), a.ns, nsValues(a.ns, nil))
	runningPod(t, r)
	return a
}

// validateFile writes obj as YAML and runs "kardinal validate -f" on it.
func validateFile(t *testing.T, e *framework.Env, obj runtime.Object) (string, error) {
	t.Helper()
	data, err := yaml.Marshal(obj)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "object.yaml")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return e.Kardinal(t, "", "validate", "-f", path)
}

// waitReady waits until Pipeline ns/name has observed its latest spec and
// its Ready condition has status, and returns the condition.
func waitReady(t *testing.T, e *framework.Env, ns, name string, status metav1.ConditionStatus) metav1.Condition {
	t.Helper()
	var cond metav1.Condition
	e.WaitPipeline(t, ns, name, time.Minute, "Ready="+string(status), func(p *v1alpha1.Pipeline) bool {
		c := meta.FindStatusCondition(p.Status.Conditions, "Ready")
		if c == nil || c.ObservedGeneration != p.Generation {
			return false
		}
		cond = *c
		return c.Status == status
	})
	return cond
}

// withType sets p's TypeMeta, which "kardinal validate" reads.
func withType(p *v1alpha1.Pipeline) *v1alpha1.Pipeline {
	p = p.DeepCopy()
	p.TypeMeta = metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Pipeline"}
	p.Namespace = ""
	return p
}

// TestDeprecated_GateWhen checks PolicyGate spec.when. "kardinal validate"
// warns that it is deprecated, the API server stores it, and a post-deploy
// gate is checked before its step starts like any other: a Bundle it refuses
// never gets a prod step or PR, and one it allows does.
//
// Covers DEP-WHEN-01.
func TestDeprecated_GateWhen(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	a := deprecatedApp(t, e, "test", "prod")

	gate := framework.Gate(a.ns, "author-check", "prod", `bundle.provenance.author != "blocked-author"`, "10s")
	gate.Spec.When = "post-deploy" //nolint:staticcheck // SA1019: the deprecated field under test
	gate.Spec.Message = "blocked-author may not deploy to prod"
	file := gate.DeepCopy()
	file.TypeMeta = metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "PolicyGate"}
	out, err := validateFile(t, e, file)
	require.NoError(t, err, "a deprecated field is a warning, not an error")
	assert.Contains(t, out, "is valid")
	assert.Contains(t, out, "warning: spec.when is deprecated and has no effect: every gate is re-checked "+
		"right before its PromotionStep starts; remove it")

	e.CreateGate(t, gate)
	var stored v1alpha1.PolicyGate
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: gate.Name}, &stored))
	assert.Equal(t, "post-deploy", stored.Spec.When, "the API server keeps the field") //nolint:staticcheck // SA1019: the field under test
	a.apply(t, a.resourcePipeline(map[string]string{"prod": "pr-review"}))

	blocked := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2, "--author", "blocked-author")
	e.WaitStepState(t, a.ns, pipelineName, blocked, "test", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, blocked, "prod", gate.Name, false, "", time.Minute)
	e.NoStep(t, a.ns, pipelineName, blocked, "prod", 30*time.Second)
	prs, err := e.Git.PullRequests(ctx, a.repo)
	require.NoError(t, err)
	assert.Empty(t, prs, "a post-deploy gate holds the change before any PR")
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))

	allowed := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3, "--author", "e2e-bot")
	e.WaitGateReady(t, a.ns, allowed, "prod", gate.Name, true, "", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, allowed, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool {
		return pr.State == "open" && strings.Contains(pr.Body, fixtures.V3)
	})
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, allowed, "prod", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("prod"), fixtures.Image+":"+fixtures.V3, syncTimeout)
}

// TestDeprecated_Regions checks spec.environments[].regions. One region has
// no effect: the Pipeline is Ready and promotes. Two set the Pipeline
// Ready=False/NotImplemented with the message "kardinal validate" prints,
// and a Bundle fails when its Graph is built, changing nothing.
//
// Covers DEP-REGIONS-01.
func TestDeprecated_Regions(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	a := deprecatedApp(t, e, "test")
	want := `environment "test": regions is not supported; declare one environment per region (prod-us, prod-eu) and use wave`

	p := a.resourcePipeline(nil)
	p.Spec.Environments[0].Regions = []string{"us-east-1"} //nolint:staticcheck // SA1019: the deprecated field under test
	out, err := validateFile(t, e, withType(p))
	require.NoError(t, err, "one region is valid")
	assert.NotContains(t, out, "regions")
	a.apply(t, p)
	waitReady(t, e, a.ns, pipelineName, metav1.ConditionTrue)
	promote(t, a, fixtures.V2, nil)

	key := types.NamespacedName{Namespace: a.ns, Name: pipelineName}
	require.NoError(t, e.Client.Get(ctx, key, p))
	p.Spec.Environments[0].Regions = []string{"us-east-1", "eu-west-1"} //nolint:staticcheck // SA1019: the deprecated field under test
	out, err = validateFile(t, e, withType(p))
	require.Error(t, err, "two regions fail validation")
	assert.Contains(t, out, want)
	require.NoError(t, e.Client.Update(ctx, p))
	cond := waitReady(t, e, a.ns, pipelineName, metav1.ConditionFalse)
	assert.Equal(t, "NotImplemented", cond.Reason)
	assert.Contains(t, cond.Message, want)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	b := e.WaitBundlePhase(t, a.ns, bundle, "Failed", promoteTimeout)
	invalid := meta.FindStatusCondition(b.Status.Conditions, "InvalidSpec")
	require.NotNil(t, invalid, "conditions: %v", b.Status.Conditions)
	assert.Contains(t, invalid.Message, "regions is not supported")
	_, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
	require.NoError(t, err)
	assert.False(t, ok, "no PromotionStep runs")
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
}

// TestDeprecated_HealthCluster checks spec.environments[].health.cluster:
// "kardinal validate" rejects it, the Pipeline is Ready=False/NotImplemented,
// and the environment's PromotionStep fails with the same message before it
// changes anything, instead of checking health in the local cluster.
//
// Covers DEP-CLUSTER-01.
func TestDeprecated_HealthCluster(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	a := deprecatedApp(t, e, "test")
	want := `environment "test": health.cluster is not supported: kardinal checks health only in the cluster it runs in`

	p := a.resourcePipeline(nil)
	p.Spec.Environments[0].Health.Cluster = "spoke" //nolint:staticcheck // SA1019: the deprecated field under test
	out, err := validateFile(t, e, withType(p))
	require.Error(t, err)
	assert.Contains(t, out, want)
	a.apply(t, p)
	cond := waitReady(t, e, a.ns, pipelineName, metav1.ConditionFalse)
	assert.Equal(t, "NotImplemented", cond.Reason)
	assert.Contains(t, cond.Message, want)
	assert.Contains(t, cond.Message, "health.type: argocd or flux")

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepMessage(t, a.ns, pipelineName, bundle, "test", "Failed",
		"health.cluster is not supported: kardinal checks health only in the cluster it runs in", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	prs, err := e.Git.PullRequests(ctx, a.repo)
	require.NoError(t, err)
	assert.Empty(t, prs)
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
}

// TestDeprecated_GitProvider checks Pipeline spec.git.provider: a Pipeline
// that names gitlab is Ready and promotes through the provider the
// controller was started with (the e2e git server), opening its PR there.
//
// Covers DEP-GITPROVIDER-01.
func TestDeprecated_GitProvider(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	ctx := context.Background()
	a := deprecatedApp(t, e, "test")
	require.NotEqual(t, "gitlab", os.Getenv(framework.EnvSCMProvider))

	p := a.resourcePipeline(map[string]string{"test": "pr-review"})
	p.Spec.Git.Provider = "gitlab" //nolint:staticcheck // SA1019: the deprecated field under test
	a.apply(t, p)
	waitReady(t, e, a.ns, pipelineName, metav1.ConditionTrue)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "promotion PR on the controller's provider", func(pr gitserver.PR) bool {
		return pr.State == "open" && strings.Contains(pr.Body, fixtures.V2)
	})
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("test"), fixtures.Image+":"+fixtures.V2, syncTimeout)
}
