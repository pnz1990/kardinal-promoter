//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The examples suite applies examples/ as their READMEs say, changing only
// what a user must: the namespace, the Git URL (the test's Forgejo repo) and
// the token Secret (the README's github-token, holding the suite's Forgejo
// token). Every other change is named where the test makes it.

// exampleTokenSecret is the Secret the examples' Pipelines read the Git token
// from (spec.git.secretRef).
const exampleTokenSecret = "github-token"

// exampleManifests reads examples/<file>, as kubectl apply -f does.
func exampleManifests(t *testing.T, file string) []*unstructured.Unstructured {
	t.Helper()
	return framework.Manifests(t, "../../../examples/"+file)
}

// exampleManifest is the one object of examples/<file>.
func exampleManifest(t *testing.T, file string) *unstructured.Unstructured {
	t.Helper()
	objs := exampleManifests(t, file)
	require.Len(t, objs, 1, "examples/%s holds one object", file)
	return objs[0]
}

// createExampleToken creates the github-token Secret the README's kubectl
// create secret makes (key token) in ns, holding the suite's Git token.
func createExampleToken(t *testing.T, e *framework.Env, ns string) {
	t.Helper()
	ctx := context.Background()
	src, err := e.Kube.CoreV1().Secrets(ns).Get(ctx, framework.GitSecretName, metav1.GetOptions{})
	require.NoError(t, err, "the suite's git token Secret")
	token := src.Data["token"]
	require.True(t, len(token) > 0, "the %s Secret has a token key", framework.GitSecretName)
	_, err = e.Kube.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: exampleTokenSecret, Namespace: ns},
		Data:       map[string][]byte{"token": token},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "create the %s Secret", exampleTokenSecret)
}

// applyExamplePipeline applies the example Pipeline p in ns over repo: the
// namespace and spec.git.url are the test's, the rest is the example's. tune,
// when set, edits the object first. It returns the Pipeline as stored.
func applyExamplePipeline(t *testing.T, e *framework.Env, p *unstructured.Unstructured, ns string, repo gitserver.Repo,
	tune func(*unstructured.Unstructured)) *v1alpha1.Pipeline {
	t.Helper()
	require.Equal(t, "Pipeline", p.GetKind())
	secret, _, _ := unstructured.NestedString(p.Object, "spec", "git", "secretRef", "name")
	require.Equal(t, exampleTokenSecret, secret, "the example reads the README's token Secret")
	branch, _, _ := unstructured.NestedString(p.Object, "spec", "git", "branch")
	require.Equal(t, repo.Branch, branch, "the example's branch is the repo's default branch")
	p.SetNamespace(ns)
	require.NoError(t, unstructured.SetNestedField(p.Object, repo.CloneURL, "spec", "git", "url"))
	if tune != nil {
		tune(p)
	}
	_, err := e.Apply(context.Background(), p)
	require.NoError(t, err, "apply Pipeline %s", p.GetName())
	var got v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: p.GetName()}, &got))
	return &got
}

// objectRef is an object to delete on cleanup.
func objectRef(apiVersion, kind, ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(apiVersion)
	u.SetKind(kind)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

// exampleArgoApp creates the Argo CD Application name, which deploys env's
// overlay of repo into dest, and waits until it deploys workload at imageV1.
// Deleting it on cleanup also deletes what it deployed, so shared namespaces
// such as dev keep nothing of the test.
func exampleArgoApp(t *testing.T, e *framework.Env, name string, repo gitserver.Repo, env, dest string) {
	t.Helper()
	e.ArgoAppSpec(t, name, map[string]interface{}{
		"project": "default",
		"source": map[string]interface{}{
			"repoURL":        repo.CloneURL,
			"targetRevision": repo.Branch,
			"path":           fixtures.Path(env),
		},
		"destination": map[string]interface{}{"server": "https://kubernetes.default.svc", "namespace": dest},
		"syncPolicy": map[string]interface{}{
			"automated": map[string]interface{}{"prune": true, "selfHeal": true},
		},
	}, true)
}

// verifiedTransition is when ps turned Verified: its Verified condition's
// lastTransitionTime.
func verifiedTransition(t *testing.T, ps *v1alpha1.PromotionStep) time.Time {
	t.Helper()
	c := findCond(ps.Status.Conditions, "Verified")
	require.Equal(t, metav1.ConditionTrue, c.Status, "step %s has Verified=True: %v", ps.Name, ps.Status.Conditions)
	return c.LastTransitionTime.Time
}

// stepNames lists ps's status.steps entries, checking each completed.
func stepNames(t *testing.T, ps *v1alpha1.PromotionStep) []string {
	t.Helper()
	var names []string
	for _, s := range ps.Status.Steps {
		names = append(names, s.Name)
		assert.Equal(t, v1alpha1.StepExecutionCompleted, s.State, "%s: %s completes", ps.Spec.Environment, s.Name)
	}
	return names
}

// demoLayoutRepo is a repo laid out like pnz1990/kardinal-demo, with podinfo
// for the image: each environments/<env> holds a kustomization.yaml that sets
// no namespace and pins the image at V1, over a deployment.yaml whose
// Deployment name hard-codes namespace default.
func demoLayoutRepo(envs []string, name string) map[string][]byte {
	files := map[string][]byte{}
	for _, env := range envs {
		files[fixtures.Path(env)+"/kustomization.yaml"] = []byte(fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
images:
  - name: %s
    newTag: %s
`, fixtures.Image, fixtures.V1))
		files[fixtures.Path(env)+"/deployment.yaml"] = []byte(strings.Replace(
			fixtures.Deployment(name, fixtures.Image+":"+fixtures.V1),
			"metadata:\n  name: "+name+"\n", "metadata:\n  name: "+name+"\n  namespace: default\n", 1))
	}
	return files
}

// TestCore_QuickstartExample runs examples/quickstart as its README does:
// the github-token Secret, the Argo CD ApplicationSet, the two org
// PolicyGates, the Pipeline, then `kardinal create bundle kardinal-test-app
// --image`. The repo is laid out like pnz1990/kardinal-demo: each overlay puts
// the kardinal-test-app Deployment in namespace default, and the
// ApplicationSet's kustomize namespace override moves each environment into
// its own kardinal-test-app-<env> namespace, so nothing lands in default.
//
// The first Bundle promotes test and uat automatically. At prod the
// require-uat-soak gate blocks with its message and the 30-minute
// expression, kardinal explain shows it as Block, and no-weekend-deploys
// passes on a weekday and blocks with its message on a weekend (UTC). No prod
// step exists while a gate is closed. The test then shortens the soak to one
// minute, since a 30-minute wait does not fit the suite, and creates a second
// Bundle, which supersedes the first. Its soak gate blocks, then opens once
// uat has been Verified for a minute; on a weekend an operator overrides
// no-weekend-deploys. prod starts no earlier than that minute, opens the
// promotion PR, and after the merge every environment runs the second
// Bundle's image.
//
// Changes to the example: the namespace and Git URL; the gates are in the
// Pipeline's namespace, not platform-policies, where every other test's prod
// would pick them up; the soak is shortened for the second Bundle; the image
// is podinfo, not kardinal-test-app.
//
// Covers EX-QUICKSTART-01.
func TestCore_QuickstartExample(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	const pipeline = "kardinal-test-app"
	envs := []string{"test", "uat", "prod"}
	workloadNS := func(env string) string { return pipeline + "-" + env }
	a := &app{e: e, ns: ns, envs: envs, repo: e.Repo(t, ns, demoLayoutRepo(envs, pipeline))}

	// Cleanup runs last first: the ApplicationSet, its Applications (which
	// carry no resources finalizer), then the namespaces Argo CD created.
	for _, env := range envs {
		e.DeleteOnCleanup(t, objectRef("v1", "Namespace", "", workloadNS(env)), true)
	}
	for _, env := range envs {
		e.DeleteOnCleanup(t, objectRef("argoproj.io/v1alpha1", "Application", framework.ArgoCDNamespace, pipeline+"-"+env), true)
	}
	appSet := exampleManifest(t, "quickstart/argocd-applications.yaml")
	require.Equal(t, "ApplicationSet", appSet.GetKind())
	require.NoError(t, unstructured.SetNestedField(appSet.Object, a.repo.CloneURL, "spec", "template", "spec", "source", "repoURL"))
	e.DeleteOnCleanup(t, appSet, true)
	_, err := e.Apply(ctx, appSet)
	require.NoError(t, err, "apply the ApplicationSet")
	for _, env := range envs {
		e.WaitArgoApp(t, pipeline+"-"+env, syncTimeout)
		e.WaitDeploymentImage(t, workloadNS(env), pipeline, imageV1, syncTimeout)
	}
	_, err = e.Kube.AppsV1().Deployments("default").Get(ctx, pipeline, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "the namespace override keeps %s out of default: %v", pipeline, err)

	createExampleToken(t, e, ns)
	gates := map[string]*unstructured.Unstructured{}
	for _, obj := range exampleManifests(t, "quickstart/policy-gates.yaml") {
		if obj.GetKind() == "Namespace" {
			assert.Equal(t, framework.PolicyNamespace, obj.GetName())
			continue
		}
		require.Equal(t, "PolicyGate", obj.GetKind())
		assert.Equal(t, framework.PolicyNamespace, obj.GetNamespace())
		obj.SetNamespace(ns)
		gates[obj.GetName()] = obj
		_, err := e.Apply(ctx, obj)
		require.NoError(t, err, "apply PolicyGate %s", obj.GetName())
	}
	require.Len(t, gates, 2, "policy-gates.yaml has two PolicyGates")
	soakMsg, _, _ := unstructured.NestedString(gates["require-uat-soak"].Object, "spec", "message")
	weekendMsg, _, _ := unstructured.NestedString(gates["no-weekend-deploys"].Object, "spec", "message")
	require.NotEmpty(t, soakMsg)
	require.NotEmpty(t, weekendMsg)
	applyExamplePipeline(t, e, exampleManifest(t, "quickstart/pipeline.yaml"), ns, a.repo, nil)

	first := e.CreateBundle(t, ns, pipeline, "--image", imageV2)
	for _, env := range []string{"test", "uat"} {
		e.WaitStepState(t, ns, pipeline, first, env, "Verified", promoteTimeout)
		e.WaitDeploymentImage(t, workloadNS(env), pipeline, imageV2, syncTimeout)
	}
	soak := e.WaitGate(t, ns, first, "prod", "require-uat-soak", gateTimeout, "blocked by the 30-minute soak",
		func(g *v1alpha1.PolicyGate) bool {
			return g.Status.LastEvaluatedAt != nil && !g.Status.Ready && strings.HasPrefix(g.Status.Reason, soakMsg+" (")
		})
	assert.Contains(t, soak.Status.Reason, "bundle.upstreamSoakMinutes >= 30")
	e.WaitExplainGate(t, ns, pipeline, "prod", "require-uat-soak", "Block", time.Minute)
	weekend := weekendGate(t, e, ns, first, weekendMsg)
	want := "Pass"
	if weekend {
		want = "Block"
	}
	e.WaitExplainGate(t, ns, pipeline, "prod", "no-weekend-deploys", want, time.Minute)
	e.NoStep(t, ns, pipeline, first, "prod", holdFor)

	require.NoError(t, unstructured.SetNestedField(gates["require-uat-soak"].Object, "bundle.upstreamSoakMinutes >= 1",
		"spec", "expression"))
	_, err = e.Apply(ctx, gates["require-uat-soak"])
	require.NoError(t, err, "shorten the soak")
	second := e.CreateBundle(t, ns, pipeline, "--image", imageV3)
	e.WaitBundlePhase(t, ns, first, "Superseded", time.Minute)
	g := e.WaitGate(t, ns, second, "prod", "require-uat-soak", gateTimeout, "blocked by the 1-minute soak",
		func(g *v1alpha1.PolicyGate) bool {
			return g.Status.LastEvaluatedAt != nil && !g.Status.Ready && strings.HasPrefix(g.Status.Reason, soakMsg+" (")
		})
	assert.Equal(t, "bundle.upstreamSoakMinutes >= 1", g.Spec.Expression, "the new Bundle's instance has the shortened soak")
	for _, env := range []string{"test", "uat"} {
		e.WaitStepState(t, ns, pipeline, second, env, "Verified", promoteTimeout)
		e.WaitDeploymentImage(t, workloadNS(env), pipeline, imageV3, syncTimeout)
	}
	if weekendGate(t, e, ns, second, weekendMsg) {
		e.MustKardinal(t, ns, "override", pipeline, "--stage", "prod", "--gate", "no-weekend-deploys",
			"--reason", "e2e: the suite runs on weekends")
		e.WaitGateReady(t, ns, second, "prod", "no-weekend-deploys", true, "", gateTimeout)
	}
	e.WaitGateReady(t, ns, second, "prod", "require-uat-soak", true, "= true", 3*time.Minute)

	prod := e.WaitStepState(t, ns, pipeline, second, "prod", "WaitingForMerge", promoteTimeout)
	uat := upstreamStatus(t, e, ns, second, "uat")
	require.NotNil(t, uat.HealthCheckedAt, "uat's health check time")
	startedAfter(t, prod, uat.HealthCheckedAt.Add(time.Minute), "prod waits until uat has soaked a minute")
	_, ok, err := e.Step(ctx, ns, pipeline, first, "prod")
	require.NoError(t, err)
	assert.False(t, ok, "the superseded Bundle never reached prod")
	assert.Equal(t, imageV1, e.DeploymentImage(t, workloadNS("prod"), pipeline), "prod waits for the merge")

	pr := a.openPR(t, second, "prod")
	assert.Equal(t, "[kardinal] Promote "+second+" to prod", pr.Title)
	a.merge(t, pr)
	e.WaitStepState(t, ns, pipeline, second, "prod", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, workloadNS("prod"), pipeline, imageV3, syncTimeout)
	e.WaitBundlePhase(t, ns, second, "Verified", time.Minute)
	for _, env := range envs {
		a.fileHas(t, env, fixtures.V3, "the second Bundle is promoted everywhere")
	}
}

// weekendGate checks the no-weekend-deploys instance of bundle's prod: on a
// Saturday or Sunday (UTC, when it was evaluated) it blocks with the gate's
// message, on any other day it passes. It reports whether it is the weekend.
func weekendGate(t *testing.T, e *framework.Env, ns, bundle, message string) bool {
	t.Helper()
	g := e.WaitGate(t, ns, bundle, "prod", "no-weekend-deploys", gateTimeout, "evaluated",
		func(g *v1alpha1.PolicyGate) bool { return g.Status.LastEvaluatedAt != nil })
	day := g.Status.LastEvaluatedAt.UTC().Weekday()
	weekend := day == time.Saturday || day == time.Sunday
	assert.Equal(t, !weekend, g.Status.Ready, "no-weekend-deploys on a %s (UTC): %s", day, framework.DescribeGate(g))
	if weekend {
		assert.True(t, strings.HasPrefix(g.Status.Reason, message+" ("), "blocked reason %q", g.Status.Reason)
	}
	return weekend
}

// TestCore_ConfigPromotionExample promotes the config Bundle of
// examples/config-promotion through its Pipeline. The platform-config repo
// has an overlay per environment, and a config change (podinfo's UI message)
// is committed on a branch; bundle.yaml is applied with that commit as
// spec.configRef. dev and staging promote automatically, health-checked on
// the platform-config Deployment in the dev and staging namespaces
// (health.type resource); prod opens a PR and, once it is merged, is
// health-checked on the platform-config-prod Argo CD Application. Each
// environment runs git-clone, config-merge, git-commit and git-push (prod
// also open-pr and wait-for-merge) and health-check, never
// kustomize-set-image; config-merge copies the overlay's three files from the
// config commit. Every Deployment gets the change and keeps its image, and
// the Bundle is Verified.
//
// Changes to the example: the namespace and Git URL, configRef.gitRepo (the
// same repo) and configRef.commitSHA (the config commit). The prod workload
// runs in the test's namespace; dev and staging are the namespaces the
// resource health check reads.
//
// Covers EX-CONFIG-01.
func TestCore_ConfigPromotionExample(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	const pipeline = "platform-config"
	envs := []string{"dev", "staging", "prod"}
	workloadNS := func(env string) string {
		if env == "prod" {
			return ns
		}
		return env
	}
	a := &app{e: e, ns: ns, envs: envs, repo: e.Repo(t, ns, fixtures.KustomizeRepoFor(envs,
		func(string) string { return pipeline }, workloadNS))}
	argoName := func(env string) string {
		if env == "prod" {
			return pipeline + "-prod" // the argocd health check's default
		}
		return ns + "-" + env
	}
	for _, env := range envs {
		if env != "prod" {
			e.EnsureNamespace(t, env)
		}
		exampleArgoApp(t, e, argoName(env), a.repo, env, workloadNS(env))
	}
	for _, env := range envs {
		e.WaitArgoApp(t, argoName(env), syncTimeout)
		e.WaitDeploymentImage(t, workloadNS(env), pipeline, imageV1, syncTimeout)
	}

	files := map[string][]byte{}
	for _, env := range envs {
		files[fixtures.Path(env)+"/deployment.yaml"] = []byte(withConfigChange(fixtures.Deployment(pipeline, imageV1)))
	}
	// Named after the test's branch: on GitHub, where the test's branch is
	// one of a shared repo, the framework writes only branches under e2e/.
	cfgBranch := a.repo.Branch + "-config-change"
	e.GitBranch(t, a.repo, cfgBranch, a.repo.Branch)
	sha, err := gitserver.CommitFiles(ctx, e.Git, a.repo, cfgBranch, "", "Set the podinfo UI message", files)
	require.NoError(t, err, "commit the config change")
	for _, env := range envs {
		require.NotContains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path(env)+"/deployment.yaml"), configValue,
			"%s on main has no config change yet", env)
	}

	createExampleToken(t, e, ns)
	applyExamplePipeline(t, e, exampleManifest(t, "config-promotion/pipeline.yaml"), ns, a.repo, nil)
	b := exampleManifest(t, "config-promotion/bundle.yaml")
	require.Equal(t, "Bundle", b.GetKind())
	b.SetNamespace(ns)
	require.NoError(t, unstructured.SetNestedField(b.Object, a.repo.CloneURL, "spec", "configRef", "gitRepo"))
	require.NoError(t, unstructured.SetNestedField(b.Object, sha, "spec", "configRef", "commitSHA"))
	_, err = e.Apply(ctx, b)
	require.NoError(t, err, "apply the Bundle")
	bundle := b.GetName()

	auto := []string{"git-clone", "config-merge", "git-commit", "git-push", "health-check"}
	for _, env := range []string{"dev", "staging"} {
		ps := e.WaitStepState(t, ns, pipeline, bundle, env, "Verified", promoteTimeout)
		assert.Equal(t, auto, stepNames(t, ps), "%s merges the config commit", env)
		assert.Equal(t, "3", ps.Status.Outputs["mergedFiles"], "%s: config-merge copies the overlay's files", env)
		waitUIMessage(t, e, workloadNS(env), pipeline)
	}
	e.WaitStepState(t, ns, pipeline, bundle, "prod", "WaitingForMerge", promoteTimeout)
	assert.NotContains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("prod")+"/deployment.yaml"), configValue,
		"prod waits for the merge")
	pr := a.openPR(t, bundle, "prod")
	assert.Equal(t, "[kardinal] Promote "+bundle+" to prod", pr.Title)
	a.merge(t, pr)
	ps := e.WaitStepState(t, ns, pipeline, bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, []string{"git-clone", "config-merge", "git-commit", "git-push", "open-pr", "wait-for-merge", "health-check"},
		stepNames(t, ps), "prod merges the config commit through a PR")
	waitUIMessage(t, e, ns, pipeline)
	for _, env := range envs {
		assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path(env)+"/deployment.yaml"), configValue,
			"%s on main has the config change", env)
		a.fileHas(t, env, fixtures.V1, "a config Bundle leaves the image pin alone")
		assert.Equal(t, imageV1, e.DeploymentImage(t, workloadNS(env), pipeline), "%s keeps its image", env)
	}
	e.WaitBundlePhase(t, ns, bundle, "Verified", time.Minute)
}

// waitUIMessage waits until the Deployment ns/name runs the config change.
func waitUIMessage(t *testing.T, e *framework.Env, ns, name string) {
	t.Helper()
	framework.Eventually(t, syncTimeout, ns+"/"+name+" runs the config change", func(ctx context.Context) (bool, string) {
		var d appsv1.Deployment
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &d); err != nil {
			return false, err.Error()
		}
		envVars := d.Spec.Template.Spec.Containers[0].Env
		ok := slices.Contains(envVars, corev1.EnvVar{Name: configVar, Value: configValue}) && deploymentAvailable(&d)
		return ok, fmt.Sprintf("env %v available=%v", envVars, deploymentAvailable(&d))
	})
}

// TestCore_WaveTopologyExample promotes a Bundle through
// examples/wave-topology: test, then staging, which bakes before it is
// Verified, then wave 1 (prod-eu and prod-us) together, then wave 2
// (prod-ap) once both of wave 1 are Verified. staging's step stays
// HealthChecking with a bake start and no prod step exists while it bakes;
// it is Verified a full bake later with "bake complete". prod-eu and prod-us
// both open their PRs after staging is Verified, while prod-ap has no step.
// prod-ap still has none once prod-eu is merged and Verified, as long as
// prod-us is not; it starts after both. Every wave-1 and wave-2 environment
// is Verified only after its own bake, and every environment runs the
// Bundle's image.
//
// Changes to the example: the namespace and Git URL, and every bake is one
// minute (the example bakes 30 minutes to 12 hours). test and staging run in
// the test and staging namespaces the resource health check reads, the prod
// environments in the test's namespace, each deployed by Argo CD Application
// wave-demo-<env>, the argocd health check's default.
//
// Covers EX-WAVE-01.
func TestCore_WaveTopologyExample(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	const pipeline = "wave-demo"
	envs := []string{"test", "staging", "prod-eu", "prod-us", "prod-ap"}
	isProd := func(env string) bool { return strings.HasPrefix(env, "prod-") }
	workloadNS := func(env string) string {
		if isProd(env) {
			return ns
		}
		return env
	}
	workload := func(env string) string {
		if isProd(env) {
			return pipeline + "-" + env
		}
		return pipeline
	}
	argoName := func(env string) string {
		if isProd(env) {
			return pipeline + "-" + env
		}
		return ns + "-" + env
	}
	a := &app{e: e, ns: ns, envs: envs, repo: e.Repo(t, ns, fixtures.KustomizeRepoFor(envs, workload, workloadNS))}
	for _, env := range envs {
		if !isProd(env) {
			e.EnsureNamespace(t, env)
		}
		exampleArgoApp(t, e, argoName(env), a.repo, env, workloadNS(env))
	}
	for _, env := range envs {
		e.WaitArgoApp(t, argoName(env), syncTimeout)
		e.WaitDeploymentImage(t, workloadNS(env), workload(env), imageV1, syncTimeout)
	}

	createExampleToken(t, e, ns)
	p := applyExamplePipeline(t, e, exampleManifest(t, "wave-topology/pipeline.yaml"), ns, a.repo,
		func(p *unstructured.Unstructured) {
			list, _, err := unstructured.NestedSlice(p.Object, "spec", "environments")
			require.NoError(t, err)
			for _, item := range list {
				if bake, ok := item.(map[string]interface{})["bake"].(map[string]interface{}); ok {
					bake["minutes"] = int64(1)
				}
			}
			require.NoError(t, unstructured.SetNestedSlice(p.Object, list, "spec", "environments"))
		})
	var got []string
	for _, env := range p.Spec.Environments {
		got = append(got, fmt.Sprintf("%s wave=%d", env.Name, env.Wave))
	}
	require.Equal(t, []string{"test wave=0", "staging wave=0", "prod-eu wave=1", "prod-us wave=1", "prod-ap wave=2"}, got)

	bundle := e.CreateBundle(t, ns, pipeline, "--image", imageV2)
	e.WaitStepState(t, ns, pipeline, bundle, "test", "Verified", promoteTimeout)
	baking := e.WaitStep(t, ns, pipeline, bundle, "staging", promoteTimeout, "the staging bake to start",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			return ps.Status.State == "HealthChecking" && ps.Status.BakeStartedAt != nil,
				fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
		})
	e.NoStep(t, ns, pipeline, bundle, "prod-eu", holdFor)
	for _, env := range envs[3:] {
		e.NoStep(t, ns, pipeline, bundle, env, time.Second)
	}
	staging := e.WaitStepState(t, ns, pipeline, bundle, "staging", "Verified", promoteTimeout)
	assert.True(t, strings.HasPrefix(staging.Status.Message, "bake complete: 1m contiguous healthy via "),
		"staging message %q", staging.Status.Message)
	assert.Equal(t, "BakeComplete", findCond(staging.Status.Conditions, "Verified").Reason)
	stagingAt := verifiedTransition(t, staging)
	require.NotNil(t, staging.Status.BakeStartedAt)
	assert.Equal(t, baking.Status.BakeStartedAt.Time, staging.Status.BakeStartedAt.Time, "one bake window")
	assert.GreaterOrEqual(t, stagingAt.Sub(staging.Status.BakeStartedAt.Time), 59*time.Second, "staging baked a minute")

	wave1 := map[string]*v1alpha1.PromotionStep{}
	for _, env := range []string{"prod-eu", "prod-us"} {
		wave1[env] = e.WaitStepState(t, ns, pipeline, bundle, env, "WaitingForMerge", promoteTimeout)
		startedAfter(t, wave1[env], stagingAt, env+" starts after staging is Verified")
	}
	e.NoStep(t, ns, pipeline, bundle, "prod-ap", holdFor)

	verified := map[string]time.Time{}
	promote := func(env string) {
		t.Helper()
		pr := a.openPR(t, bundle, env)
		assert.Equal(t, "[kardinal] Promote "+bundle+" to "+env, pr.Title)
		a.merge(t, pr)
		ps := e.WaitStepState(t, ns, pipeline, bundle, env, "Verified", promoteTimeout)
		assert.True(t, strings.HasPrefix(ps.Status.Message, "bake complete: 1m contiguous healthy via "),
			"%s message %q", env, ps.Status.Message)
		verified[env] = verifiedTransition(t, ps)
		require.NotNil(t, ps.Status.BakeStartedAt, "%s baked", env)
		assert.GreaterOrEqual(t, verified[env].Sub(ps.Status.BakeStartedAt.Time), 59*time.Second, "%s baked a minute", env)
	}
	promote("prod-eu")
	us, ok, err := e.Step(context.Background(), ns, pipeline, bundle, "prod-us")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "WaitingForMerge", us.Status.State, "prod-us is not merged yet")
	e.NoStep(t, ns, pipeline, bundle, "prod-ap", holdFor)
	promote("prod-us")
	ap := e.WaitStepState(t, ns, pipeline, bundle, "prod-ap", "WaitingForMerge", promoteTimeout)
	for _, env := range []string{"prod-eu", "prod-us"} {
		startedAfter(t, ap, verified[env], "prod-ap starts after "+env+" is Verified")
	}
	promote("prod-ap")
	e.WaitBundlePhase(t, ns, bundle, "Verified", time.Minute)
	for _, env := range envs {
		e.WaitDeploymentImage(t, workloadNS(env), workload(env), imageV2, syncTimeout)
		a.fileHas(t, env, fixtures.V2, "the Bundle is promoted everywhere")
	}
}
