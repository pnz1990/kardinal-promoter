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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// TestPipeline_ReadyAndPhase checks the status of a valid Pipeline. It is
// Ready=True (Valid) and its phase is Unknown before any Bundle runs. While
// a Bundle is held at prod by a gate, after test is Verified, the phase is
// Promoting, in status and in the Phase column; once an operator overrides
// the gate and prod is Verified, the phase is Ready.
//
// Covers PIPE-READY-01, PIPE-PHASE-01.
func TestPipeline_ReadyAndPhase(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	require.NoError(t, e.Client.Create(context.Background(), operatorHold(a.ns, "prod")))
	a.apply(t, a.pipeline(nil))
	p := waitPipeline(t, e, a.ns, pipelineName, time.Minute, "Ready=True Valid", func(p *v1alpha1.Pipeline) (bool, string) {
		ok, seen := framework.CondIs(p.Status.Conditions, "Ready", metav1.ConditionTrue, "Valid")
		return ok && p.Status.Phase != "", fmt.Sprintf("%s phase=%q", seen, p.Status.Phase)
	})
	ready := findCond(p.Status.Conditions, "Ready")
	assert.Equal(t, "Pipeline spec is valid", ready.Message)
	assert.Equal(t, p.Generation, ready.ObservedGeneration, "Ready describes the current spec")
	assert.Equal(t, "Unknown", p.Status.Phase, "no Bundle has run")
	assert.Equal(t, "Unknown", pipelinePhaseColumn(t, a), "the Phase column")

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	a.waitPipelinePhase(t, "Promoting")
	framework.Consistently(t, 15*time.Second, "Promoting while the gate holds prod", func(ctx context.Context) (bool, string) {
		var cur v1alpha1.Pipeline
		if err := e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: pipelineName}, &cur); err != nil {
			return false, err.Error()
		}
		_, held, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil {
			return false, err.Error()
		}
		return cur.Status.Phase == "Promoting" && !held, fmt.Sprintf("phase=%q prod step=%t", cur.Status.Phase, held)
	})
	assert.Equal(t, "Promoting", pipelinePhaseColumn(t, a), "the Phase column")
	a.fileHas(t, "prod", fixtures.V1, "prod is held")

	a.overrideHold(t, bundle, "prod")
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	a.waitPipelinePhase(t, "Ready")
	assert.Equal(t, "Ready", pipelinePhaseColumn(t, a), "the Phase column")
	a.running(t, "prod", imageV2, "prod is promoted after the override")
}

// TestPipeline_ValidationFailed checks the two Pipeline errors about its own
// spec and credentials. The API server refuses two environments with the same
// name, so a duplicate never reaches the controller (docs/changelog.md said it
// set Ready=False). A git.secretRef in another namespace, even one that holds
// a copy of the token Secret, sets Ready=False ValidationFailed naming both
// namespaces. A Bundle's step fails with the same message before any built-in
// step runs, so the Secret is never read and nothing is committed.
//
// Covers PIPE-READY-02.
func TestPipeline_ValidationFailed(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	other := e.Namespace(t)
	a := &app{e: e, ns: ns, envs: []string{"test"},
		repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}}))}
	ctx := context.Background()

	dup := a.pipeline(nil)
	dup.Spec.Environments = append(dup.Spec.Environments, dup.Spec.Environments[0])
	msg := rejected(t, e, dup)
	assert.Contains(t, msg, "Duplicate value", "duplicate environment names")
	assert.Contains(t, msg, `"name":"test"`)

	before, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 10)
	require.NoError(t, err)
	p := a.pipeline(nil)
	p.Spec.Git.SecretRef.Namespace = other
	a.apply(t, p)
	want := fmt.Sprintf("git.secretRef.namespace %q is not allowed: the Secret must be in the Pipeline's namespace %q", other, ns)
	waitPipeline(t, e, ns, pipelineName, time.Minute, "Ready=False ValidationFailed", func(p *v1alpha1.Pipeline) (bool, string) {
		ok, seen := framework.CondIs(p.Status.Conditions, "Ready", metav1.ConditionFalse, "ValidationFailed")
		return ok && findCond(p.Status.Conditions, "Ready").Message == want, seen
	})

	bundle := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
	ps := e.WaitStepState(t, ns, pipelineName, bundle, "test", "Failed", time.Minute)
	assert.Equal(t, want, ps.Status.Message)
	assert.Empty(t, ps.Status.Steps, "no built-in step runs, so nothing reads the Secret")
	e.WaitBundlePhase(t, ns, bundle, "Failed", time.Minute)
	after, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 10)
	require.NoError(t, err)
	assert.Equal(t, len(before), len(after), "nothing is committed")
}

// TestPipeline_NotImplemented checks a value the controller accepts but does
// not implement: a health.resource.kind other than Deployment. The Pipeline
// gets Ready=False NotImplemented with the documented message, and its
// Bundle's step fails with it before any git change: nothing is committed.
//
// Covers PIPE-NOTIMPL-01.
func TestPipeline_NotImplemented(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	a := &app{e: e, ns: ns, envs: []string{"test"},
		repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}}))}
	ctx := context.Background()
	before, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 10)
	require.NoError(t, err)

	const prefix = "not implemented or not supported, so a Bundle fails when it reaches an environment that uses one: "
	const kindMsg = `health.resource.kind "StatefulSet" is not supported: only Deployment is checked`
	cases := []struct {
		name string
		set  func(p *v1alpha1.Pipeline, env *v1alpha1.EnvironmentSpec)
		// ready is in the Ready message; step is the failed step's message.
		ready, step string
	}{{
		name: "kind",
		set: func(_ *v1alpha1.Pipeline, env *v1alpha1.EnvironmentSpec) {
			env.Health = v1alpha1.HealthConfig{Type: "resource",
				Resource: &v1alpha1.ResourceRef{Kind: "StatefulSet", Name: fixtures.Workload("test"), Namespace: ns}}
		},
		ready: `environment "test": ` + kindMsg,
		step:  kindMsg,
	}}
	for _, c := range cases {
		p := a.pipeline(nil)
		p.Name = c.name
		c.set(p, envSpec(t, p, "test"))
		a.apply(t, p)
		got := waitPipeline(t, e, ns, c.name, time.Minute, "Ready=False NotImplemented", func(p *v1alpha1.Pipeline) (bool, string) {
			return framework.CondIs(p.Status.Conditions, "Ready", metav1.ConditionFalse, "NotImplemented")
		})
		msg := findCond(got.Status.Conditions, "Ready").Message
		assert.True(t, strings.HasPrefix(msg, prefix), "%s: Ready message %q", c.name, msg)
		assert.Contains(t, msg, c.ready, c.name)

		bundle := e.CreateBundle(t, ns, c.name, "--image", imageV2)
		ps := e.WaitStepState(t, ns, c.name, bundle, "test", "Failed", time.Minute)
		assert.Contains(t, ps.Status.Message, c.step, c.name)
		e.WaitBundlePhase(t, ns, bundle, "Failed", time.Minute)
	}
	after, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 10)
	require.NoError(t, err)
	assert.Equal(t, len(before), len(after), "nothing is committed")
}

// TestPipeline_RejectedFields checks what the API server refuses at apply
// time, each with a message that says what to do: autoRollback,
// update.strategy argocd with approval pr-review, and reserved environment
// names.
//
// Covers PIPE-REJECT-01.
func TestPipeline_RejectedFields(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	// A repository of its own: Pipelines of other tests at the same path
	// would get PathConflict.
	a := &app{e: e, ns: ns, envs: []string{"test"}, repo: gitserver.Repo{CloneURL: "https://git.example.com/" + ns + "/podinfo.git", Branch: "main"}}
	cases := []struct {
		name string
		set  func(p *v1alpha1.Pipeline, env *v1alpha1.EnvironmentSpec)
		want string
	}{{
		name: "autoRollback",
		set: func(_ *v1alpha1.Pipeline, env *v1alpha1.EnvironmentSpec) {
			env.AutoRollback = &v1alpha1.AutoRollbackSpec{FailureThreshold: 3} //nolint:staticcheck // SA1019: set to check it is rejected
		},
		want: "environments[].autoRollback is not implemented; remove it (automatic rollback is configured with " +
			"onHealthFailure, see docs/rollback.md)",
	}, {
		name: "argocd with pr-review",
		set: func(_ *v1alpha1.Pipeline, env *v1alpha1.EnvironmentSpec) {
			env.Update.Strategy = "argocd"
			env.Approval = "pr-review"
		},
		want: "environments[]: update.strategy argocd patches the Application directly and cannot honour approval: " +
			"pr-review; use approval: auto with a PolicyGate, or a git-based strategy (kustomize or helm) for a reviewed promotion",
	}}
	for _, name := range []string{"bundle", "time", "spec", "self", "true", "kro"} {
		cases = append(cases, struct {
			name string
			set  func(p *v1alpha1.Pipeline, env *v1alpha1.EnvironmentSpec)
			want string
		}{
			name: "name " + name,
			set:  func(_ *v1alpha1.Pipeline, env *v1alpha1.EnvironmentSpec) { env.Name = name },
			want: "reserved environment name: the name becomes a kro Graph node ID; bundle, time, kro reserved IDs (spec, " +
				"status, metadata, graph, self, each, item, ...) and CEL keywords are not allowed; rename the environment",
		})
	}
	for _, c := range cases {
		p := a.pipeline(nil)
		c.set(p, &p.Spec.Environments[0])
		assert.Contains(t, rejected(t, e, p), c.want, c.name)
	}
	assert.NoError(t, e.Client.Create(context.Background(), a.pipeline(nil), client.DryRunAll),
		"the same Pipeline without them is accepted")
}

// TestPipeline_NameAndDurationRules checks the environment name and duration
// rules the API server enforces: a name must be a DNS label of at most 63
// characters and unique, and health.timeout and waitForMergeTimeout must be
// Go durations. Values that follow the rules are accepted.
//
// Covers PIPE-NAME-01.
func TestPipeline_NameAndDurationRules(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	a := &app{e: e, ns: ns, envs: []string{"test", "prod"},
		repo: gitserver.Repo{CloneURL: "https://git.example.com/" + ns + "/podinfo.git", Branch: "main"}}
	const dnsLabel = "should match '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$'"
	cases := []struct {
		name string
		set  func(env *v1alpha1.EnvironmentSpec)
		want []string
	}{
		{"upper case", func(env *v1alpha1.EnvironmentSpec) { env.Name = "Prod" }, []string{"spec.environments[1].name", dnsLabel}},
		{"underscore", func(env *v1alpha1.EnvironmentSpec) { env.Name = "prod_eu" }, []string{"spec.environments[1].name", dnsLabel}},
		{"leading dash", func(env *v1alpha1.EnvironmentSpec) { env.Name = "-prod" }, []string{"spec.environments[1].name", dnsLabel}},
		{"64 characters", func(env *v1alpha1.EnvironmentSpec) { env.Name = strings.Repeat("p", 64) },
			[]string{"spec.environments[1].name", "Too long", "63"}},
		{"duplicate", func(env *v1alpha1.EnvironmentSpec) { env.Name = "test" }, []string{"spec.environments[1]", "Duplicate value"}},
		{"health.timeout", func(env *v1alpha1.EnvironmentSpec) { env.Health.Timeout = "15 minutes" },
			[]string{"spec.environments[1].health.timeout", "should match"}},
		{"waitForMergeTimeout", func(env *v1alpha1.EnvironmentSpec) { env.WaitForMergeTimeout = "1 day" },
			[]string{"spec.environments[1].waitForMergeTimeout", "should match"}},
	}
	for _, c := range cases {
		p := a.pipeline(nil)
		c.set(&p.Spec.Environments[1])
		msg := rejected(t, e, p)
		for _, want := range c.want {
			assert.Contains(t, msg, want, c.name)
		}
	}

	ok := a.pipeline(nil)
	prod := &ok.Spec.Environments[1]
	prod.Name = "prod-eu-1" + strings.Repeat("x", 54) // 63 characters
	prod.Health.Timeout = "1h30m"
	prod.WaitForMergeTimeout = "72h"
	assert.NoError(t, e.Client.Create(context.Background(), ok, client.DryRunAll), "a 63-character name and Go durations")
}

// TestPipeline_PathStaysInRepo checks that an environment path cannot leave
// the repository: an absolute path, a path that climbs out with .. and a
// path through a symlink that points outside the checkout each fail the
// step, and nothing is committed.
//
// Covers PIPE-PATH-01.
func TestPipeline_PathStaysInRepo(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	a := &app{e: e, ns: ns, envs: []string{"test"},
		repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}}))}
	e.PushTree(t, a.repo, "add a symlink to outside the repository", func(dir string) {
		require.NoError(t, os.Symlink("../..", filepath.Join(dir, "environments", "escape")))
	})
	ctx := context.Background()
	before, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 10)
	require.NoError(t, err)

	cases := []struct{ name, path, want string }{
		{"absolute", "/etc/podinfo", `path "/etc/podinfo" must be relative to the repository root`},
		{"dotdot", "environments/../../podinfo", `path "environments/../../podinfo" must stay inside the repository`},
		{"symlink", "environments/escape", "path escapes from parent"},
	}
	for _, c := range cases {
		p := a.pipeline(nil)
		p.Name = c.name
		p.Spec.Environments[0].Path = c.path
		a.apply(t, p)
		bundle := e.CreateBundle(t, ns, c.name, "--image", imageV2)
		ps := e.WaitStepState(t, ns, c.name, bundle, "test", "Failed", time.Minute)
		assert.Contains(t, ps.Status.Message, c.want, c.name)
		e.WaitBundlePhase(t, ns, bundle, "Failed", time.Minute)
	}
	after, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 10)
	require.NoError(t, err)
	assert.Equal(t, len(before), len(after), "nothing is committed")
	a.fileHas(t, "test", fixtures.V1, "the environment is untouched")
}

// TestPipeline_DeploymentMetrics checks status.deploymentMetrics. A Pipeline
// with no Bundle Verified in prod has none, so the documented staleProdDays
// -1 never shows. Then three Bundles reach prod, which bakes for a minute:
// V2; V3, held by a gate an operator overrides; and a rollback to V2. After
// each, the metrics count the rollouts and the sample, the override (1 of 2,
// then 1 of 3), the rollback (1 of 3, though it was not automatic), 0 stale
// days, and the p50 and p90 of the whole minutes from each Bundle's creation
// to its prod step's Verified condition.
//
// Covers PIPE-DORA-01.
func TestPipeline_DeploymentMetrics(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	ctx := context.Background()
	p := a.pipeline(nil)
	envSpec(t, p, "prod").Bake = &v1alpha1.BakeConfig{Minutes: 1}
	a.apply(t, p)
	cur := waitPipeline(t, e, a.ns, pipelineName, time.Minute, "Ready=True", func(p *v1alpha1.Pipeline) (bool, string) {
		return framework.CondIs(p.Status.Conditions, "Ready", metav1.ConditionTrue, "Valid")
	})
	assert.Nil(t, cur.Status.DeploymentMetrics, "no metrics before a Bundle is Verified in prod")

	var bundles []string
	promote := func(bundle string) {
		t.Helper()
		e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
		e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
		bundles = append(bundles, bundle)
	}
	check := func(interventions, rollbacks int) {
		t.Helper()
		n := len(bundles)
		got := waitPipeline(t, e, a.ns, pipelineName, 2*time.Minute, fmt.Sprintf("metrics of %d Bundles", n),
			func(p *v1alpha1.Pipeline) (bool, string) {
				m := p.Status.DeploymentMetrics
				return m != nil && m.SampleSize == n && m.OperatorInterventionRateMillis == interventions &&
					m.AutoRollbackRateMillis == rollbacks, fmt.Sprintf("%+v", m)
			}).Status.DeploymentMetrics
		var leads []int64
		for _, b := range bundles {
			leads = append(leads, leadMinutes(t, a, b))
		}
		sort.Slice(leads, func(i, j int) bool { return leads[i] < leads[j] })
		assert.Equal(t, n, got.RolloutsLast30Days, "rollouts")
		assert.Equal(t, 0, got.StaleProdDays, "prod was promoted today")
		assert.Equal(t, leads[n*50/100], got.P50CommitToProdMinutes, "p50 of %v", leads)
		assert.Equal(t, leads[n*90/100], got.P90CommitToProdMinutes, "p90 of %v", leads)
		assert.GreaterOrEqual(t, got.P50CommitToProdMinutes, int64(1), "the prod bake alone takes a minute")
		assert.NotNil(t, got.ComputedAt)
	}

	promote(e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2))
	check(0, 0)

	gate := operatorHold(a.ns, "prod")
	require.NoError(t, e.Client.Create(ctx, gate))
	held := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV3)
	e.WaitStepState(t, a.ns, pipelineName, held, "test", "Verified", promoteTimeout)
	a.overrideHold(t, held, "prod")
	promote(held)
	require.NoError(t, e.Client.Delete(ctx, gate))
	check(500, 0)

	out := e.MustKardinal(t, a.ns, "rollback", pipelineName, "--env", "prod")
	m := regexp.MustCompile(`Bundle (\S+) created \(rollbackOf=(\S+)\)`).FindStringSubmatch(out)
	require.NotNil(t, m, "rollback output:\n%s", out)
	assert.Equal(t, bundles[0], m[2], "the rollback restores the V2 Bundle")
	promote(m[1])
	a.running(t, "prod", imageV2, "the rollback is deployed")
	check(333, 333)
}

// operatorHold is a PolicyGate named hold that blocks env until an operator
// overrides it.
func operatorHold(ns, env string) *v1alpha1.PolicyGate {
	return &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "hold", Namespace: ns, Labels: map[string]string{"kardinal.io/applies-to": env}},
		Spec:       v1alpha1.PolicyGateSpec{Expression: "false", Message: "held for an operator", RecheckInterval: "10s"},
	}
}

// overrideHold waits for bundle's instance of the hold gate and overrides it
// at env with `kardinal override`, as an operator would.
func (a *app) overrideHold(t *testing.T, bundle, env string) {
	t.Helper()
	framework.Eventually(t, time.Minute, "the hold gate instance of "+bundle, func(ctx context.Context) (bool, string) {
		var gates v1alpha1.PolicyGateList
		if err := a.e.Client.List(ctx, &gates, client.InNamespace(a.ns), client.MatchingLabels{
			"kardinal.io/bundle": bundle, "kardinal.io/gate-template": "hold"}); err != nil {
			return false, err.Error()
		}
		return len(gates.Items) == 1, fmt.Sprintf("%d instances", len(gates.Items))
	})
	a.e.MustKardinal(t, a.ns, "override", pipelineName, "--stage", env, "--gate", "hold", "--reason", "e2e operator override")
}

// waitPipelinePhase waits until the app's Pipeline has status.phase phase.
func (a *app) waitPipelinePhase(t *testing.T, phase string) {
	t.Helper()
	waitPipeline(t, a.e, a.ns, pipelineName, time.Minute, "phase "+phase, func(p *v1alpha1.Pipeline) (bool, string) {
		return p.Status.Phase == phase, "phase=" + p.Status.Phase
	})
}

// pipelinePhaseColumn is the Phase column `kubectl get pipelines` shows for
// the app's Pipeline.
func pipelinePhaseColumn(t *testing.T, a *app) string {
	t.Helper()
	return a.e.GetTable(t, "kardinal.io", "v1alpha1", "pipelines", a.ns).Cell(pipelineName, "Phase")
}

// rejected dry-runs creating p and returns the API server's error message,
// failing the test when p is accepted.
func rejected(t *testing.T, e *framework.Env, p *v1alpha1.Pipeline) string {
	t.Helper()
	err := e.Client.Create(context.Background(), p, client.DryRunAll)
	if err == nil {
		t.Errorf("Pipeline %s was accepted: %+v", p.Name, p.Spec.Environments)
		return ""
	}
	return err.Error()
}

// leadMinutes is a Bundle's commit-to-prod time as the controller counts it:
// whole minutes from the Bundle's creation to its prod step's Verified
// condition.
func leadMinutes(t *testing.T, a *app, bundle string) int64 {
	t.Helper()
	ps, ok, err := a.e.Step(context.Background(), a.ns, pipelineName, bundle, "prod")
	require.NoError(t, err)
	require.True(t, ok, "%s has a prod step", bundle)
	c := findCond(ps.Status.Conditions, "Verified")
	require.Equal(t, metav1.ConditionTrue, c.Status, "%s prod step Verified condition", bundle)
	return int64(c.LastTransitionTime.Sub(a.bundle(t, bundle).CreationTimestamp.Time).Minutes())
}

// TestPipeline_GitSecretNotReferenceable checks #1506: a git Secret without
// kardinal.io/referenceable=true is not used. The Pipeline is Ready=False
// with reason SecretNotReferenceable and the kubectl label command, and its
// Bundle's step waits (Promoting, not Failed) with the same command and no
// commit. Labeling the Secret makes the Pipeline Ready and lets the step
// promote.
//
// Covers PIPE-GITSECRET-LABEL-01.
func TestPipeline_GitSecretNotReferenceable(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	var secret corev1.Secret
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: framework.GitSecretName}, &secret))
	unlabeled := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unlabeled-git", Namespace: a.ns}, Data: secret.Data}
	require.NoError(t, e.Client.Create(ctx, unlabeled))
	p := a.pipeline(nil)
	p.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: "unlabeled-git"}
	a.apply(t, p)

	label := "kubectl label secret unlabeled-git -n " + a.ns + " kardinal.io/referenceable=true"
	waitPipeline(t, e, a.ns, pipelineName, time.Minute, "Ready=False SecretNotReferenceable", func(p *v1alpha1.Pipeline) (bool, string) {
		c := meta.FindStatusCondition(p.Status.Conditions, "Ready")
		return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "SecretNotReferenceable" && strings.Contains(c.Message, label),
			fmt.Sprintf("%+v", c)
	})
	before, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 1)
	require.NoError(t, err)
	require.NotEmpty(t, before)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStep(t, a.ns, pipelineName, bundle, "test", time.Minute, "waiting for its git Secret", func(ps *v1alpha1.PromotionStep) (bool, string) {
		return ps.Status.State == "Promoting" && strings.Contains(ps.Status.Message, label), ps.Status.State + ": " + ps.Status.Message
	})
	assert.Equal(t, "waiting for the git Secret: git Secret "+a.ns+"/unlabeled-git is not labeled kardinal.io/referenceable=true, "+
		"so git does not use it; label it: "+label, ps.Status.Message)
	after, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 1)
	require.NoError(t, err)
	require.NotEmpty(t, after)
	assert.Equal(t, before[0].SHA, after[0].SHA, "nothing is committed with an unlabeled Secret")

	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: "unlabeled-git"}, unlabeled))
	unlabeled.Labels = map[string]string{"kardinal.io/referenceable": "true"}
	require.NoError(t, e.Client.Update(ctx, unlabeled))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	a.running(t, "test", imageV2, "the labeled Secret is used")
	// The Secret is not watched; the Pipeline is re-checked every minute.
	waitPipeline(t, e, a.ns, pipelineName, 2*time.Minute, "Ready once labeled", func(p *v1alpha1.Pipeline) (bool, string) {
		c := meta.FindStatusCondition(p.Status.Conditions, "Ready")
		return c != nil && c.Status == metav1.ConditionTrue, fmt.Sprintf("%+v", c)
	})
}

// TestPipeline_FailedVersionIsVisible: when the newest version fails before
// any step (here its Graph cannot be built: a verification needs Argo
// Rollouts, which the core suite does not install), the reason is where a
// user looks, not only on the Bundle. The Pipeline's status.failedVersion
// names the Bundle, its reason and message; status.summary says the version
// failed before it started; kardinal why leads with it, with the fix and the
// command that shows the whole record; kardinal status prints an ERROR line.
// Fixing the Pipeline retries the version, and once it is live the
// failure is gone everywhere.
//
// Covers PIPE-FAILEDVERSION-01.
func TestPipeline_FailedVersionIsVisible(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	first := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitBundlePhase(t, a.ns, first, "Verified", promoteTimeout)

	var p v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &p))
	p.Spec.Environments[0].Verification = &v1alpha1.VerificationSpec{AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "smoke"}}}
	require.NoError(t, e.Client.Update(ctx, &p))
	failed := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitBundle(t, a.ns, failed, time.Minute, "Failed with GraphBuildFailed", failedWith("GraphBuildFailed", "AnalysisRun is not served"))

	label := fixtures.V3 + " (" + failed + ")"
	got := waitPipeline(t, e, a.ns, pipelineName, time.Minute, "status.failedVersion names "+failed, func(p *v1alpha1.Pipeline) (bool, string) {
		f := p.Status.FailedVersion
		return f != nil && f.Bundle == failed && f.Reason == "GraphBuildFailed", fmt.Sprintf("failedVersion %+v", f)
	})
	f := got.Status.FailedVersion
	assert.Equal(t, fixtures.V3, f.Version)
	assert.Contains(t, f.Message, "AnalysisRun is not served")
	assert.Empty(t, f.Environment, "it failed before any step")
	assert.NotNil(t, f.Since)
	assert.Equal(t, fixtures.V2+" live in test; "+label+" failed before it started (GraphBuildFailed)", got.Status.Summary)

	why := e.MustKardinal(t, a.ns, "why", pipelineName)
	assert.True(t, strings.HasPrefix(why, "The newest version "+label+" failed before it started: "), why)
	assert.Contains(t, why, "AnalysisRun is not served")
	assert.Regexp(t, `(?m)^  Reason\s+GraphBuildFailed$`, why)
	assert.Regexp(t, `(?m)^  Fix\s+fix the pipeline, version or gate the message names; changing the pipeline or its gates retries this version$`, why)
	assert.Regexp(t, `(?m)^  See\s+kubectl get bundle `+failed+` -n `+a.ns+` -o yaml$`, why)
	status := e.MustKardinal(t, a.ns, "status", pipelineName)
	assert.Contains(t, status, "ERROR: the newest version "+label+" failed before it started (GraphBuildFailed): ")
	assert.Contains(t, status, "kardinal why "+pipelineName+" says what fixes it")

	// Fix the Pipeline: the version is retried, goes live, and the failure
	// is gone.
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &p))
	p.Spec.Environments[0].Verification = nil
	require.NoError(t, e.Client.Update(ctx, &p))
	e.WaitBundlePhase(t, a.ns, failed, "Verified", promoteTimeout)
	waitPipeline(t, e, a.ns, pipelineName, time.Minute, "status.failedVersion cleared", func(p *v1alpha1.Pipeline) (bool, string) {
		return p.Status.FailedVersion == nil && p.Status.Summary == fixtures.V3+" live in test", fmt.Sprintf("failedVersion %+v, summary %q", p.Status.FailedVersion, p.Status.Summary)
	})
	assert.NotContains(t, e.MustKardinal(t, a.ns, "why", pipelineName), "failed")
}
