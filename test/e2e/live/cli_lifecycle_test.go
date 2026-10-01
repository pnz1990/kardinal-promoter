//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The tests in this file change a pipeline's course with the CLI: rollback,
// promote, delete bundle, config Bundles, create bundle --dry-run, pause and
// resume.

// emergencyNotice is what the deprecated rollback --emergency prints.
const emergencyNotice = "Flag --emergency has been deprecated, it has no effect; use kardinal override to pass a blocking gate\n"

// refuses runs a kardinal command that must fail: exit 1, want on stderr,
// nothing on stdout.
func refuses(t *testing.T, c *framework.CLI, ns, want string, args ...string) {
	t.Helper()
	r := c.Fail(ns, args...)
	assert.Equal(t, 1, r.Code, "kardinal %v", args)
	assert.Equal(t, want+"\n", r.Stderr, "kardinal %v", args)
	assert.Empty(t, r.Stdout, "kardinal %v", args)
}

// bundleNames lists the names of the Bundles in ns.
func bundleNames(t *testing.T, e *framework.Env, ns string) []string {
	t.Helper()
	var names []string
	for _, b := range bundles(t, e, ns) {
		names = append(names, b.Name)
	}
	return names
}

// assertSameEnvFiles asserts that environments/<env> on repo's branch holds
// the files src holds at ref.
func assertSameEnvFiles(t *testing.T, e *framework.Env, repo, src gitserver.Repo, ref, env string) {
	t.Helper()
	for _, f := range []string{"kustomization.yaml", "deployment.yaml", "service.yaml"} {
		path := fixtures.Path(env) + "/" + f
		assert.Equal(t, e.ReadFile(t, src, ref, path), e.ReadFile(t, repo, repo.Branch, path), "%s is %s@%s's", path, src.Name, ref)
	}
}

// waitBundleGone waits until Bundle name and what its promotion created (the
// Graph, the PromotionSteps and the gate instances, which the Bundle owns)
// are gone.
func waitBundleGone(t *testing.T, e *framework.Env, ns, name string) {
	t.Helper()
	framework.Eventually(t, time.Minute, "Bundle "+name+" and its Graph, steps and gates deleted", func(ctx context.Context) (bool, string) {
		var b v1alpha1.Bundle
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); !apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("Bundle: err=%v phase=%q", err, b.Status.Phase)
		}
		owned := []client.ListOption{client.InNamespace(ns), client.MatchingLabels{"kardinal.io/bundle": name}}
		var steps v1alpha1.PromotionStepList
		if err := e.Client.List(ctx, &steps, owned...); err != nil || len(steps.Items) > 0 {
			return false, fmt.Sprintf("%d PromotionSteps (err=%v)", len(steps.Items), err)
		}
		var gates v1alpha1.PolicyGateList
		if err := e.Client.List(ctx, &gates, owned...); err != nil || len(gates.Items) > 0 {
			return false, fmt.Sprintf("%d gate instances (err=%v)", len(gates.Items), err)
		}
		graphs := &unstructured.UnstructuredList{}
		graphs.SetGroupVersionKind(graph.GraphGVK.GroupVersion().WithKind(graph.GraphGVK.Kind + "List"))
		if err := e.Client.List(ctx, graphs, owned...); err != nil || len(graphs.Items) > 0 {
			return false, fmt.Sprintf("%d Graphs (err=%v)", len(graphs.Items), err)
		}
		return true, ""
	})
}

// TestCLI_Rollback rolls prod back with kardinal rollback. Before anything
// has reached prod, and for an unknown environment or Pipeline, it refuses
// and creates nothing. --to refuses the Bundle prod runs, a missing Bundle
// and one never Verified in prod. Without --to it restores the newest other
// Bundle Verified in prod: it prints what it rolls back from and to and the
// new Bundle, which records the Bundle it rolls back from, who asked for it
// and the target's provenance, goes through test again and redeploys the old
// version in both environments. history marks its rows as rollbacks. A
// second rollback has nothing older to restore, and --to restores a newer
// Bundle. --emergency prints a deprecation notice and changes nothing.
// Covers CLI-ROLLBACK-01, CLI-EMERGENCY-01.
func TestCLI_Rollback(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(nil))
	waitPipelineValid(t, e, a.ns, pipelineName)

	refuses(t, c, a.ns, "rollback: nothing has been deployed to prod in pipeline podinfo yet: conflict",
		"rollback", pipelineName, "--env", "prod")
	refuses(t, c, a.ns, `rollback: pipeline podinfo has no environment "staging": invalid request`,
		"rollback", pipelineName, "--env", "staging")
	refuses(t, c, a.ns, "rollback: pipeline "+a.ns+"/nope: not found", "rollback", "nope", "--env", "prod")
	refuses(t, c, a.ns, `required flag(s) "env" not set`, "rollback", pipelineName)
	assert.Empty(t, bundles(t, e, a.ns), "a refused rollback creates nothing")

	// A, then B, reach prod; C reaches only test.
	bA := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2,
		"--commit", "0a1b2c3", "--author", "e2e-bot", "--ci-run-url", "https://ci.example/run/7")
	e.WaitStepState(t, a.ns, pipelineName, bA, "prod", "Verified", 2*promoteTimeout)
	bB := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitStepState(t, a.ns, pipelineName, bB, "prod", "Verified", 2*promoteTimeout)
	bC := e.CreateBundleObject(t, &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Namespace: a.ns},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: pipelineName,
			Images: []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V1}},
			Intent: &v1alpha1.BundleIntent{TargetEnvironment: "test"}},
	})
	e.WaitBundlePhase(t, a.ns, bC, "Verified", promoteTimeout)
	_, ok, err := e.Step(context.Background(), a.ns, pipelineName, bC, "prod")
	require.NoError(t, err)
	require.False(t, ok, "%s targets test only", bC)
	assertEnvAt(t, a, "test", fixtures.V1)
	assertEnvAt(t, a, "prod", fixtures.V3)

	refuses(t, c, a.ns, "rollback: bundle "+bB+" is what prod runs now: conflict",
		"rollback", pipelineName, "--env", "prod", "--to", bB)
	refuses(t, c, a.ns, "rollback: bundle "+a.ns+"/nope: not found",
		"rollback", pipelineName, "--env", "prod", "--to", "nope")
	refuses(t, c, a.ns, "rollback: bundle "+bC+" was never Verified in prod, so it is not known to work there; pick a Bundle that was: invalid request",
		"rollback", pipelineName, "--env", "prod", "--to", bC)
	assert.Len(t, bundles(t, e, a.ns), 3, "a refused rollback creates nothing")

	// The default target is A, the newest other Bundle Verified in prod.
	r := c.Run(a.ns, "rollback", pipelineName, "--env", "prod", "--emergency")
	require.Equal(t, 0, r.Code, r.Output())
	assert.Equal(t, emergencyNotice, r.Stderr)
	m := regexp.MustCompile(`^Rolling back podinfo in prod from ` + bB + ` to ` + bA + ` \(` + regexp.QuoteMeta(fixtures.Image+":"+fixtures.V2) + `\)\n` +
		`Bundle (podinfo-rollback-[a-z0-9]{5}) created \(rollbackOf=` + bA + `\)\n` +
		`Track with: kardinal explain podinfo --env prod\n$`).FindStringSubmatch(r.Stdout)
	require.NotNil(t, m, "rollback output:\n%s", r.Stdout)
	bR := m[1]
	rb := getBundle(t, e, a.ns, bR)
	assert.Equal(t, "true", rb.Labels["kardinal.io/rollback"])
	assert.Equal(t, pipelineName, rb.Labels["kardinal.io/pipeline"])
	assert.NotContains(t, rb.Labels, "kardinal.io/emergency", "--emergency has no effect")
	assert.Equal(t, bB, rb.Annotations["kardinal.io/rollback-from"])
	assert.Equal(t, cliUser(t), rb.Annotations["kardinal.io/requested-by"])
	assert.NotEmpty(t, rb.Annotations["kardinal.io/created-at"])
	assert.Equal(t, "image", rb.Spec.Type)
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}, rb.Spec.Images)
	assert.Equal(t, &v1alpha1.BundleIntent{TargetEnvironment: "prod"}, rb.Spec.Intent)
	require.NotNil(t, rb.Spec.Provenance)
	assert.Equal(t, v1alpha1.BundleProvenance{CommitSHA: "0a1b2c3", Author: "e2e-bot", CIRunURL: "https://ci.example/run/7", RollbackOf: bA},
		*rb.Spec.Provenance, "the rollback Bundle carries the target's provenance")

	// The rollback goes through test again, then prod.
	e.WaitStepState(t, a.ns, pipelineName, bR, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bR, "prod", "Verified", promoteTimeout)
	for _, env := range []string{"test", "prod"} {
		assertEnvAt(t, a, env, fixtures.V2)
		assert.Equal(t, fixtures.V2, kustomizeTag(t, a, env))
	}
	hist := framework.ParseTable(c.Must(a.ns, "history", pipelineName))
	actions := map[string]string{}
	for _, row := range hist {
		if prev, ok := actions[row["BUNDLE"]]; ok {
			assert.Equal(t, prev, row["ACTION"], "every row of %s has one action", row["BUNDLE"])
		}
		actions[row["BUNDLE"]] = row["ACTION"]
	}
	assert.Equal(t, map[string]string{bA: "promote", bB: "promote", bC: "promote", bR: "rollback"}, actions, "history:\n%v", hist)

	// Nothing older is left to restore: B was rolled back from, and A
	// deploys what prod runs now.
	noTarget := "rollback: no earlier Bundle with artifacts, not already rolled back from, was Verified in prod (deployed now: " + bR + "); pick one with --to: conflict"
	refuses(t, c, a.ns, noTarget, "rollback", pipelineName, "--env", "prod")
	r = c.Fail(a.ns, "rollback", pipelineName, "--env", "prod", "--emergency")
	assert.Equal(t, emergencyNotice+noTarget+"\n", r.Stderr, "--emergency does not lift a refusal")
	refuses(t, c, a.ns, "rollback: bundle "+bA+" deploys the same artifacts as the deployed bundle "+bR+": conflict",
		"rollback", pipelineName, "--env", "prod", "--to", bA)
	assert.Len(t, bundles(t, e, a.ns), 4, "a refused rollback creates nothing")

	// --to restores any Bundle Verified in prod, here the newer B.
	out := c.Must(a.ns, "rollback", pipelineName, "--env", "prod", "--to", bB)
	m = regexp.MustCompile(`^Rolling back podinfo in prod from ` + bR + ` to ` + bB + ` \(` + regexp.QuoteMeta(fixtures.Image+":"+fixtures.V3) + `\)\n` +
		`Bundle (podinfo-rollback-[a-z0-9]{5}) created \(rollbackOf=` + bB + `\)\n` +
		`Track with: kardinal explain podinfo --env prod\n$`).FindStringSubmatch(out)
	require.NotNil(t, m, "rollback --to output:\n%s", out)
	bR2 := m[1]
	rb2 := getBundle(t, e, a.ns, bR2)
	assert.Equal(t, bR, rb2.Annotations["kardinal.io/rollback-from"])
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V3}}, rb2.Spec.Images)
	require.NotNil(t, rb2.Spec.Provenance)
	assert.Equal(t, v1alpha1.BundleProvenance{RollbackOf: bB}, *rb2.Spec.Provenance, "B has no provenance to copy")
	e.WaitStepState(t, a.ns, pipelineName, bR2, "prod", "Verified", 2*promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V3)
}

// TestCLI_Promote promotes to prod with kardinal promote. It refuses, and
// creates nothing, for the first environment, before any Bundle is Verified
// in test, for an unknown environment or Pipeline, and while a newer Bundle
// is still promoting. Otherwise it creates a Bundle for prod from the newest
// Bundle Verified in test, with the same images and provenance, prints both
// names and goes through test (a no-op there) and prod's PR review. While
// that Bundle promotes, and once it is Verified in prod, promote refuses
// again.
// Covers CLI-PROMOTE-01.
func TestCLI_Promote(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	hold := framework.Gate(a.ns, "hold", "test", openExpr, recheck)
	e.CreateGate(t, hold)
	waitPipelineValid(t, e, a.ns, pipelineName)

	refuses(t, c, a.ns, "promote: test is the first environment of pipeline podinfo, so there is nothing upstream to promote; create a Bundle with `kardinal create bundle`: invalid request",
		"promote", pipelineName, "-e", "test")
	refuses(t, c, a.ns, "promote: no Bundle with artifacts is Verified in test yet: conflict", "promote", pipelineName, "-e", "prod")
	refuses(t, c, a.ns, `promote: pipeline podinfo has no environment "staging": invalid request`, "promote", pipelineName, "--env", "staging")
	refuses(t, c, a.ns, "promote: pipeline "+a.ns+"/nope: not found", "promote", "nope", "-e", "prod")
	refuses(t, c, a.ns, `required flag(s) "env" not set`, "promote", pipelineName)
	refuses(t, c, a.ns, "--env is required", "promote", pipelineName, "--env", "")
	assert.Empty(t, bundles(t, e, a.ns), "a refused promote creates nothing")

	// A is Verified in test only; B, newer, is held at test's gate.
	bA := e.CreateBundleObject(t, &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Labels: map[string]string{openLabel: "true"}},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: pipelineName,
			Images:     []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}},
			Provenance: &v1alpha1.BundleProvenance{CommitSHA: "4d5e6f7", Author: "e2e-bot", CIRunURL: "https://ci.example/run/9"},
			Intent:     &v1alpha1.BundleIntent{TargetEnvironment: "test"}},
	})
	e.WaitBundlePhase(t, a.ns, bA, "Verified", promoteTimeout)
	assertEnvAt(t, a, "test", fixtures.V2)
	head := e.BranchHead(t, a.repo)
	bB := e.CreateBundleObject(t, &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Namespace: a.ns},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: pipelineName,
			Images: []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V3}},
			Intent: &v1alpha1.BundleIntent{TargetEnvironment: "test"}},
	})
	e.WaitGateReady(t, a.ns, bB, "test", "hold", false, "", gateTimeout)
	refuses(t, c, a.ns, "promote: bundle "+bB+" is still promoting and a new Bundle would supersede it; wait for it to finish: conflict",
		"promote", pipelineName, "-e", "prod")
	assert.Equal(t, "Bundle "+bB+" deleted\n", c.Must(a.ns, "delete", "bundle", bB))
	waitBundleGone(t, e, a.ns, bB)
	require.NoError(t, e.Client.Delete(ctx, hold))

	// promote creates P from A, the newest Bundle Verified in test.
	out := c.Must(a.ns, "promote", pipelineName, "-e", "prod")
	m := regexp.MustCompile(`^Promoting podinfo to prod: bundle (podinfo-[a-z0-9]{5}) created from ` + bA +
		` \(Verified in test; ` + regexp.QuoteMeta(fixtures.Image+":"+fixtures.V2) + `\)\n` +
		`Track with: kardinal get bundles podinfo\n$`).FindStringSubmatch(out)
	require.NotNil(t, m, "promote output:\n%s", out)
	bP := m[1]
	p := getBundle(t, e, a.ns, bP)
	assert.Equal(t, pipelineName, p.Labels["kardinal.io/pipeline"])
	assert.NotContains(t, p.Labels, openLabel, "labels are not copied")
	assert.Equal(t, bA, p.Annotations["kardinal.io/promoted-from"])
	assert.Equal(t, cliUser(t), p.Annotations["kardinal.io/requested-by"])
	assert.NotEmpty(t, p.Annotations["kardinal.io/created-at"])
	assert.Equal(t, "image", p.Spec.Type)
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}, p.Spec.Images)
	assert.Equal(t, &v1alpha1.BundleIntent{TargetEnvironment: "prod"}, p.Spec.Intent)
	require.NotNil(t, p.Spec.Provenance)
	assert.Equal(t, v1alpha1.BundleProvenance{CommitSHA: "4d5e6f7", Author: "e2e-bot", CIRunURL: "https://ci.example/run/9"}, *p.Spec.Provenance)

	// test already runs A's version, so P's test step changes nothing.
	e.WaitStepState(t, a.ns, pipelineName, bP, "test", "Verified", promoteTimeout)
	assert.Equal(t, head, e.BranchHead(t, a.repo), "P's test step pushes no commit")
	e.WaitStepState(t, a.ns, pipelineName, bP, "prod", "WaitingForMerge", promoteTimeout)
	refuses(t, c, a.ns, "promote: bundle "+bP+" is already being promoted to prod: conflict", "promote", pipelineName, "-e", "prod")
	pr := openPR(t, e, a.repo, fixtures.V2)
	assertEnvAt(t, a, "prod", fixtures.V1)
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, bP, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, bP, "Verified", time.Minute)
	assertEnvAt(t, a, "prod", fixtures.V2)
	refuses(t, c, a.ns, "promote: bundle "+bP+", the newest Bundle Verified in test, is already Verified in prod: conflict",
		"promote", pipelineName, "-e", "prod")
	assert.ElementsMatch(t, []string{bA, bP}, bundleNames(t, e, a.ns))
}

// TestCLI_DeleteBundle deletes a Bundle waiting for its prod PR with kardinal
// delete bundle: it prints the name, and the Bundle, its Graph, steps and
// gate instances are gone from the cluster and from get bundles and get
// steps, with git unchanged. A missing Bundle is an error; "bundles" is an
// alias. kardinal approve, deprecated, fails with a pointer to kardinal
// override and leaves the Bundle and its step as they were.
// Covers CLI-DELETE-BUNDLE-01, CLI-APPROVE-01.
func TestCLI_DeleteBundle(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	waitPipelineValid(t, e, a.ns, pipelineName)

	bA := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bA, "prod", "WaitingForMerge", 2*promoteTimeout)
	openPR(t, e, a.repo, fixtures.V2)
	before := getBundle(t, e, a.ns, bA)

	// approve changes nothing.
	r := c.Fail(a.ns, "approve", bA, "--env", "prod")
	assert.Equal(t, 1, r.Code)
	assert.Empty(t, r.Stdout)
	assert.Equal(t, "Command \"approve\" is deprecated, it has no effect; use `kardinal override` to force-pass a gate\n"+
		"kardinal approve is deprecated and has no effect: it only labelled the Bundle, and no gate ever read the label. "+
		"To force-pass a gate with an audit record, run: kardinal override <pipeline> --stage <environment> --gate <gate-name> --reason <text>\n",
		r.Stderr)
	after := getBundle(t, e, a.ns, bA)
	assert.Equal(t, before.Labels, after.Labels, "approve does not label the Bundle")
	assert.NotContains(t, after.Labels, "kardinal.io/approved")
	ps, ok, err := e.Step(ctx, a.ns, pipelineName, bA, "prod")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "WaitingForMerge", ps.Status.State, "approve does not move the step")

	head := e.BranchHead(t, a.repo)
	assert.Equal(t, "Bundle "+bA+" deleted\n", c.Must(a.ns, "delete", "bundle", bA))
	waitBundleGone(t, e, a.ns, bA)
	assert.Equal(t, "BUNDLE   TYPE   PHASE   AGE\n", c.Must(a.ns, "get", "bundles"))
	assert.Equal(t, "No active bundles for pipeline \"podinfo\".\n", c.Must(a.ns, "get", "steps", pipelineName))
	assert.Equal(t, head, e.BranchHead(t, a.repo), "deleting a Bundle writes nothing to git")
	assertEnvAt(t, a, "prod", fixtures.V1)

	refuses(t, c, a.ns, fmt.Sprintf("bundle %q not found in namespace %s", bA, a.ns), "delete", "bundle", bA)
	bB := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	assert.Equal(t, "Bundle "+bB+" deleted\n", c.Must(a.ns, "delete", "bundles", bB))
	waitBundleGone(t, e, a.ns, bB)
	assert.Empty(t, bundles(t, e, a.ns))
}

// TestCLI_ConfigBundle promotes config Bundles made with kardinal create
// bundle --type config. A config or mixed Bundle without --config-commit, or
// a mixed one without --image, is refused. --config-repo names the repository
// the commit is read from: git-clone checks it out, config-merge copies the
// environment's directory from it, and test runs what that commit holds.
// Without --config-repo the commit is read from the Pipeline's own
// repository.
// Covers CLI-CREATE-BUNDLE-02.
func TestCLI_ConfigBundle(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	waitPipelineValid(t, e, a.ns, pipelineName)
	initSHA := e.BranchHead(t, a.repo)
	cfg := e.Repo(t, a.ns+"-cfg", fixtures.KustomizeRepo(fixtures.App{Namespace: a.ns, Envs: []string{"test"}, Tag: fixtures.V2}))
	cfgSHA := e.BranchHead(t, cfg)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--type", "config"}, `create bundle: type "config" requires --config-commit`},
		{[]string{"--type", "config", "--config-repo", cfg.CloneURL}, `create bundle: type "config" requires --config-commit`},
		{[]string{"--type", "mixed", "--config-commit", cfgSHA}, `create bundle: type "mixed" requires at least one --image`},
		{[]string{"--type", "mixed", "--image", fixtures.Image + ":" + fixtures.V2}, `create bundle: type "mixed" requires --config-commit`},
	} {
		refuses(t, c, a.ns, tc.want, append([]string{"create", "bundle", pipelineName}, tc.args...)...)
	}
	assert.Empty(t, bundles(t, e, a.ns), "a refused create bundle creates nothing")

	// The commit is read from --config-repo.
	out := c.Must(a.ns, "create", "bundle", pipelineName, "--type", "config", "--config-commit", cfgSHA, "--config-repo", cfg.CloneURL)
	m := regexp.MustCompile(`^Bundle (podinfo-[a-z0-9]{5}) created for pipeline podinfo\n`).FindStringSubmatch(out)
	require.NotNil(t, m, "create bundle output:\n%s", out)
	b1 := m[1]
	got := getBundle(t, e, a.ns, b1)
	assert.Equal(t, "config", got.Spec.Type)
	assert.Empty(t, got.Spec.Images)
	assert.Equal(t, &v1alpha1.ConfigRef{GitRepo: cfg.CloneURL, CommitSHA: cfgSHA}, got.Spec.ConfigRef)
	ps := e.WaitStepState(t, a.ns, pipelineName, b1, "test", "Verified", promoteTimeout)
	var names []string
	for _, s := range ps.Status.Steps {
		names = append(names, s.Name)
	}
	assert.Equal(t, []string{"git-clone", "config-merge", "git-commit", "git-push", "health-check"}, names)
	head1 := e.BranchHead(t, a.repo)
	assert.NotEqual(t, initSHA, head1, "b1 pushes a commit")
	assertSameEnvFiles(t, e, a.repo, cfg, cfgSHA, "test")
	assertEnvAt(t, a, "test", fixtures.V2)

	// Without --config-repo the commit is read from the Pipeline's repo:
	// its first commit puts test back at V1.
	out = c.Must(a.ns, "create", "bundle", pipelineName, "--type", "config", "--config-commit", initSHA)
	m = regexp.MustCompile(`^Bundle (podinfo-[a-z0-9]{5}) created for pipeline podinfo\n`).FindStringSubmatch(out)
	require.NotNil(t, m, "create bundle output:\n%s", out)
	b2 := m[1]
	assert.Equal(t, &v1alpha1.ConfigRef{CommitSHA: initSHA}, getBundle(t, e, a.ns, b2).Spec.ConfigRef)
	e.WaitStepState(t, a.ns, pipelineName, b2, "test", "Verified", promoteTimeout)
	assert.NotEqual(t, head1, e.BranchHead(t, a.repo), "b2 pushes a commit")
	assertSameEnvFiles(t, e, a.repo, a.repo, initSHA, "test")
	assertEnvAt(t, a, "test", fixtures.V1)
}

// TestCLI_CreateBundleDryRun previews a Bundle with kardinal create bundle
// --dry-run: it prints the Graph's size and the environments in promotion
// order with their gates, read from the cluster, and creates nothing. Bad
// flags and a missing Pipeline are errors.
// Covers CLI-CREATE-DRYRUN-01.
func TestCLI_CreateBundleDryRun(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(nil))
	e.CreateGate(t, framework.Gate(a.ns, "change-freeze", "prod", openExpr, recheck))
	waitPipelineValid(t, e, a.ns, pipelineName)

	want := "[DRY-RUN] Bundle \"podinfo-dry-run\" for pipeline \"podinfo\"\n\n" +
		"Promotion graph: 6 node(s)\n\n" +
		"Environments in promotion order:\n" +
		"  • test\n" +
		"  • prod (gates: change-freeze)\n\n" +
		"No resources were created. Remove --dry-run to apply.\n"
	head := e.BranchHead(t, a.repo)
	r := c.Run(a.ns, "create", "bundle", pipelineName, "--image", fixtures.Image+":"+fixtures.V2, "--dry-run")
	require.Equal(t, 0, r.Code, r.Output())
	assert.Equal(t, want, r.Stdout)
	assert.Empty(t, r.Stderr)
	assert.Equal(t, want, c.Must(a.ns, "create", "bundle", pipelineName, "--type", "config", "--config-commit", head, "--dry-run"))

	refuses(t, c, a.ns, fmt.Sprintf("dry-run: pipeline %q not found in namespace %q", "nope", a.ns),
		"create", "bundle", "nope", "--image", fixtures.Image+":"+fixtures.V2, "--dry-run")
	refuses(t, c, a.ns, `create bundle: type "config" requires --config-commit`,
		"create", "bundle", pipelineName, "--type", "config", "--dry-run")
	refuses(t, c, a.ns, `create bundle: type "image" requires at least one --image`,
		"create", "bundle", pipelineName, "--dry-run")

	framework.Consistently(t, holdFor, "no Bundle, PromotionStep or git change after --dry-run", func(ctx context.Context) (bool, string) {
		var bl v1alpha1.BundleList
		var steps v1alpha1.PromotionStepList
		if err := e.Client.List(ctx, &bl, client.InNamespace(a.ns)); err != nil {
			return false, err.Error()
		}
		if err := e.Client.List(ctx, &steps, client.InNamespace(a.ns)); err != nil {
			return false, err.Error()
		}
		return len(bl.Items) == 0 && len(steps.Items) == 0, fmt.Sprintf("%d Bundles, %d PromotionSteps", len(bl.Items), len(steps.Items))
	})
	assert.Equal(t, head, e.BranchHead(t, a.repo))
	assertEnvAt(t, a, "test", fixtures.V1)
}

// TestCLI_PauseResume pauses and resumes a pipeline with kardinal pause and
// kardinal resume. pause prints that the pipeline is paused, sets
// spec.paused and creates the freeze gate; get pipelines marks it [PAUSED]
// and a new Bundle's step holds in Pending. resume prints that it resumed,
// clears spec.paused and removes the gate, and the held step promotes. Both
// are idempotent; a missing Pipeline is an error.
// Covers CLI-PAUSE-01, CLI-RESUME-01.
func TestCLI_PauseResume(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	waitPipelineValid(t, e, a.ns, pipelineName)

	paused := "Pipeline podinfo paused. No new promotions will start; in-flight steps hold at the next safe point.\n"
	refuses(t, c, a.ns, "pause nope: pipeline "+a.ns+"/nope: not found", "pause", "nope")
	refuses(t, c, a.ns, "resume nope: pipeline "+a.ns+"/nope: not found", "resume", "nope")
	refuses(t, c, a.ns, "accepts 1 arg(s), received 0", "pause")

	assert.Equal(t, paused, c.Must(a.ns, "pause", pipelineName))
	p := getPipeline(t, a)
	assert.True(t, p.Spec.Paused, "pause sets spec.paused")
	g := e.WaitFreezeGate(t, a.ns, pipelineName, 10*time.Second)
	assertFreezeGate(t, g, p)
	assert.True(t, g.Spec.Generated, "the freeze gate is not a gate template")
	assert.Equal(t, paused, c.Must(a.ns, "pause", pipelineName), "pausing a paused pipeline is a no-op")
	assert.True(t, getPipeline(t, a).Spec.Paused)
	e.WaitPipeline(t, a.ns, pipelineName, gateTimeout, "Paused", pausedCondition)
	row := framework.TableRow(c.Must(a.ns, "get", "pipelines"), "PIPELINE", pipelineName+" [PAUSED]")
	require.NotNil(t, row, "get pipelines marks the pipeline paused")

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepMessage(t, a.ns, pipelineName, bundle, "test", "Pending", pausedMsg, time.Minute)
	e.StepHeld(t, a.ns, pipelineName, bundle, "test", pausedMsg, holdFor)
	assertEnvAt(t, a, "test", fixtures.V1)

	assert.Equal(t, "Pipeline podinfo resumed.\n", c.Must(a.ns, "resume", pipelineName))
	assert.False(t, getPipeline(t, a).Spec.Paused, "resume clears spec.paused")
	e.WaitNoFreezeGate(t, a.ns, pipelineName, 10*time.Second)
	assert.Equal(t, "Pipeline podinfo resumed.\n", c.Must(a.ns, "resume", pipelineName), "resuming a running pipeline is a no-op")
	e.WaitPipeline(t, a.ns, pipelineName, gateTimeout, "without a Paused condition", noPausedCondition)
	assert.NotNil(t, framework.TableRow(c.Must(a.ns, "get", "pipelines"), "PIPELINE", pipelineName))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", resumeTimeout+promoteTimeout)
	assertEnvAt(t, a, "test", fixtures.V2)
}
