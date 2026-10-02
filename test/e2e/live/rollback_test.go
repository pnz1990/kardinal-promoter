//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The actors the controller records on the rollback Bundles it creates
// (annotation kardinal.io/requested-by and the PR's "Rolled back by").
const (
	alarmActor  = "kardinal-controller (onHealthFailure=rollback)"
	policyActor = "kardinal-controller (auto-rollback via RollbackPolicy)"
)

// rbCreated reads the rollback Bundle and its target from kardinal
// rollback's output.
var rbCreated = regexp.MustCompile(`(?m)^Bundle (\S+) created \(rollbackOf=(\S+)\)$`)

// rbRollback runs kardinal rollback for env with args and returns its output
// and the rollback Bundle's name.
func rbRollback(t *testing.T, a *app, env string, args ...string) (string, string) {
	t.Helper()
	out := a.e.MustKardinal(t, a.ns, append([]string{"rollback", pipelineName, "--env", env}, args...)...)
	m := rbCreated.FindStringSubmatch(out)
	require.Len(t, m, 3, "kardinal rollback names the Bundle it created:\n%s", out)
	return out, m[1]
}

// rbRefused runs kardinal rollback with args, which must fail, and returns
// what it printed: the error alone.
func rbRefused(t *testing.T, a *app, args ...string) string {
	t.Helper()
	out, err := a.e.Kardinal(t, a.ns, append([]string{"rollback", pipelineName}, args...)...)
	require.Error(t, err, "kardinal rollback %v must fail:\n%s", args, out)
	return strings.TrimSpace(out)
}

// rbOutput is kardinal rollback's output for a rollback of env from the
// Bundle deployed now to target, as rb deploying artifacts.
func rbOutput(env, from, target, artifacts, rb string) string {
	return fmt.Sprintf("Rolling back %s in %s from %s to %s (%s)\nBundle %s created (rollbackOf=%s)\nTrack with: kardinal explain %s --env %s\n",
		pipelineName, env, from, target, artifacts, rb, target, pipelineName, env)
}

// rbOnlyBundles checks that the namespace has exactly the Bundles want.
func rbOnlyBundles(t *testing.T, e *framework.Env, ns string, want ...string) {
	t.Helper()
	assert.ElementsMatch(t, want, bundleNames(t, e, ns), "the Bundles in %s", ns)
}

// rbCountBundles counts the namespace's Bundles. It does not fail the test,
// so a Consistently check can call it.
func rbCountBundles(ctx context.Context, e *framework.Env, ns string) (int, error) {
	var list v1alpha1.BundleList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return 0, fmt.Errorf("list Bundles in %s: %w", ns, err)
	}
	return len(list.Items), nil
}

// rbAssertBundle checks the rollback Bundle rb of env against the documented
// shape: the rollback and pipeline labels, kardinal.io/reason only for an
// automatic rollback, the replaced Bundle and the actor in annotations, the
// target's provenance with rollbackOf, its type, and the environment as the
// intent's target.
func rbAssertBundle(t *testing.T, e *framework.Env, ns, rb, env, from, target, actor, reason string) *v1alpha1.Bundle {
	t.Helper()
	b := getBundle(t, e, ns, rb)
	of := getBundle(t, e, ns, target)
	assert.Equal(t, "true", b.Labels["kardinal.io/rollback"], "label kardinal.io/rollback")
	assert.Equal(t, pipelineName, b.Labels["kardinal.io/pipeline"], "label kardinal.io/pipeline")
	got, ok := b.Labels["kardinal.io/reason"]
	if reason == "" {
		assert.False(t, ok, "a manual rollback has no kardinal.io/reason label, got %q", got)
	} else {
		assert.Equal(t, reason, got, "label kardinal.io/reason")
	}
	assert.Equal(t, from, b.Annotations["kardinal.io/rollback-from"], "annotation kardinal.io/rollback-from")
	assert.Equal(t, actor, b.Annotations["kardinal.io/requested-by"], "annotation kardinal.io/requested-by")
	assert.Equal(t, of.Spec.Type, b.Spec.Type)
	require.NotNil(t, b.Spec.Provenance)
	assert.Equal(t, target, b.Spec.Provenance.RollbackOf, "spec.provenance.rollbackOf")
	want := of.Spec.Provenance
	if want == nil {
		want = &v1alpha1.BundleProvenance{}
	}
	assert.Equal(t, want.CommitSHA, b.Spec.Provenance.CommitSHA, "provenance.commitSHA is the restored build's")
	assert.Equal(t, want.CIRunURL, b.Spec.Provenance.CIRunURL, "provenance.ciRunURL is the restored build's")
	assert.Equal(t, want.Author, b.Spec.Provenance.Author, "provenance.author is the restored build's author, not the actor")
	require.NotNil(t, b.Spec.Intent)
	assert.Equal(t, env, b.Spec.Intent.TargetEnvironment, "spec.intent.targetEnvironment")
	return b
}

// rbNoAudit checks that no AuditEvent in ns has action.
func rbNoAudit(t *testing.T, e *framework.Env, ns, action string) {
	t.Helper()
	var list v1alpha1.AuditEventList
	require.NoError(t, e.Client.List(context.Background(), &list, client.InNamespace(ns),
		client.MatchingLabels{"kardinal.io/action": action}))
	assert.Empty(t, list.Items, "no %s AuditEvent in %s", action, ns)
}

// rbReason is the events with reason. A repeated Event is one object with a
// series, so each is a distinct occurrence.
func rbReason(events []eventsv1.Event, reason string) []eventsv1.Event {
	var out []eventsv1.Event
	for _, ev := range events {
		if ev.Reason == reason {
			out = append(out, ev)
		}
	}
	return out
}

// rbHistory is kardinal history's rows, keyed by its columns BUNDLE, ACTION,
// ENV, PR, DURATION and TIMESTAMP.
func rbHistory(t *testing.T, a *app) []map[string]string {
	t.Helper()
	return framework.ParseTable(a.e.MustKardinal(t, a.ns, "history", pipelineName))
}

// rbHistoryRow is a history row's BUNDLE, ACTION, ENV and PR.
func rbHistoryRow(r map[string]string) []string {
	return []string{r["BUNDLE"], r["ACTION"], r["ENV"], r["PR"]}
}

// rbFinished is a history DURATION of a finished step: a whole number of a
// unit.
var rbFinished = regexp.MustCompile(`^[0-9]+[smhd]$`)

// rbMerge waits for the Bundle's PR for env, merges it and waits for the
// step to be Verified. It returns the PR as it was before the merge.
func rbMerge(t *testing.T, a *app, bundle, env string) gitserver.PR {
	t.Helper()
	a.e.WaitStepState(t, a.ns, pipelineName, bundle, env, "WaitingForMerge", promoteTimeout)
	head := "kardinal/" + bundle + "/" + env
	pr := a.e.WaitPR(t, a.repo, time.Minute, "the PR from "+head, func(pr gitserver.PR) bool {
		return pr.Head == head && pr.State == "open"
	})
	require.NoError(t, a.e.Git.MergePR(context.Background(), a.repo, pr.Number))
	a.e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Verified", promoteTimeout)
	return pr
}

// rbVerified waits until the Bundle's step for each env is Verified, in
// order, and then the Bundle.
func rbVerified(t *testing.T, a *app, bundle string, envs ...string) {
	t.Helper()
	for _, env := range envs {
		a.e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Verified", promoteTimeout)
	}
	a.e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// rbAliasApp is an app whose test environment runs three Deployments,
// podinfo-a, podinfo-b and podinfo-c, from one kustomization. Each container
// names its image by a kustomize alias, the Deployment's name, that newName
// maps to fixtures.Image, so a Bundle can change one of them and leave the
// others. The Pipeline is a.pipeline.
func rbAliasApp(t *testing.T, e *framework.Env) *app {
	t.Helper()
	ns := e.Namespace(t)
	names := []string{"a", "b", "c"}
	base := fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: names})
	dir := fixtures.Path("test")
	kust := fmt.Sprintf("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nnamespace: %s\nresources:\n", ns)
	images := "images:\n"
	files := map[string][]byte{}
	for _, n := range names {
		w := fixtures.Workload(n)
		files[dir+"/"+n+".yaml"] = []byte(strings.Replace(string(base[fixtures.Path(n)+"/deployment.yaml"]),
			"image: "+fixtures.Image+":"+fixtures.V1, "image: "+w, 1))
		kust += "  - " + n + ".yaml\n"
		images += fmt.Sprintf("  - name: %s\n    newName: %s\n    newTag: %s\n", w, fixtures.Image, fixtures.V1)
	}
	files[dir+"/kustomization.yaml"] = []byte(kust + images)
	a := &app{e: e, ns: ns, envs: []string{"test"}, repo: e.Repo(t, ns, files)}
	e.ArgoApp(t, a.argoApp("test"), a.repo, dir, ns)
	e.WaitArgoApp(t, a.argoApp("test"), syncTimeout)
	for _, n := range names {
		e.WaitDeploymentImage(t, ns, fixtures.Workload(n), fixtures.Image+":"+fixtures.V1, syncTimeout)
	}
	a.apply(t, a.pipeline(nil))
	return a
}

// rbAliases reads the images of test's kustomization: alias -> newName:newTag.
func rbAliases(t *testing.T, a *app) map[string]string {
	t.Helper()
	var k struct {
		Images []struct {
			Name    string `json:"name"`
			NewName string `json:"newName"`
			NewTag  string `json:"newTag"`
		} `json:"images"`
	}
	raw := a.e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/kustomization.yaml")
	require.NoError(t, yaml.Unmarshal([]byte(raw), &k), raw)
	out := map[string]string{}
	for _, img := range k.Images {
		out[img.Name] = img.NewName + ":" + img.NewTag
	}
	return out
}

// rbAliasesAt checks git and the running Deployments: podinfo-a, podinfo-b
// and podinfo-c at the versions given in that order.
func rbAliasesAt(t *testing.T, a *app, versions ...string) {
	t.Helper()
	want := map[string]string{}
	for i, n := range []string{"a", "b", "c"} {
		want[fixtures.Workload(n)] = fixtures.Image + ":" + versions[i]
	}
	assert.Equal(t, want, rbAliases(t, a), "the kustomization keeps each alias and its newName")
	for i, n := range []string{"a", "b", "c"} {
		a.e.WaitDeploymentImage(t, a.ns, fixtures.Workload(n), fixtures.Image+":"+versions[i], syncTimeout)
	}
}

// TestRollback_RestoresUnnamedImages settles what a rollback does with images
// the target Bundle does not name (matrix: "left as they are"; docs: the
// deployed Bundle's images get the newest earlier Verified version, or the
// rollback is refused). An image neither Bundle names is left as it is. An
// image the deployed Bundle changed and the target does not name gets the
// version of the newest earlier Verified Bundle that names it, and with no
// such Bundle the rollback is refused, naming the image, and creates nothing.
//
// Covers RB-RESTORE-01.
func TestRollback_RestoresUnnamedImages(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := rbAliasApp(t, e)
	promote := func(alias, version string) string {
		b := e.CreateBundle(t, a.ns, pipelineName, "--image", alias+":"+version)
		rbVerified(t, a, b, "test")
		return b
	}

	b1 := promote(fixtures.Workload("a"), fixtures.V2)
	b2 := promote(fixtures.Workload("b"), fixtures.V2)
	rbAliasesAt(t, a, fixtures.V2, fixtures.V2, fixtures.V1)
	assert.Equal(t, fmt.Sprintf("rollback: no Bundle other than %[1]s with image podinfo-b was Verified in test, so a rollback "+
		"to %[2]s would leave podinfo-b at the deployed version; roll back with a Bundle that names it: conflict", b2, b1),
		rbRefused(t, a, "--env", "test"))
	rbOnlyBundles(t, e, a.ns, b1, b2)

	b3 := promote(fixtures.Workload("b"), fixtures.V1)
	b4 := promote(fixtures.Workload("a"), fixtures.V3)
	b5 := promote(fixtures.Workload("b"), fixtures.V3)
	rbAliasesAt(t, a, fixtures.V3, fixtures.V3, fixtures.V1)

	// b4 names only podinfo-a; podinfo-b comes from b3, the newest earlier
	// Verified Bundle that names it.
	out, rb := rbRollback(t, a, "test")
	assert.Equal(t, rbOutput("test", b5, b4, "podinfo-a:"+fixtures.V3+", podinfo-b:"+fixtures.V1, rb), out)
	b := rbAssertBundle(t, e, a.ns, rb, "test", b5, b4, cliUser(t), "")
	assert.Equal(t, []v1alpha1.ImageRef{
		{Repository: fixtures.Workload("a"), Tag: fixtures.V3},
		{Repository: fixtures.Workload("b"), Tag: fixtures.V1},
	}, b.Spec.Images)
	rbVerified(t, a, rb, "test")
	rbAliasesAt(t, a, fixtures.V3, fixtures.V1, fixtures.V1)
	rbOnlyBundles(t, e, a.ns, b1, b2, b3, b4, b5, rb)
}

// TestRollback_PullRequest checks the rollback PR in a pr-review
// environment: the title says what it restores, the labels mark it as a
// rollback, and the body names the Bundle and version it replaces (FROM),
// the one it restores (TO) and who rolled back, with the restored build's
// provenance. The PR branch has the old version and the environment keeps
// the new one until the merge; after it, the old version runs, and kardinal
// history shows the rollback with its PR.
//
// Covers RB-PR-01.
func TestRollback_PullRequest(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))
	kust := fixtures.Path("test") + "/kustomization.yaml"

	b1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2,
		"--commit", "0123abc", "--author", "e2e-bot", "--ci-run-url", "https://ci.example/run/1")
	pr1 := rbMerge(t, a, b1, "test")
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	pr2 := rbMerge(t, a, b2, "test")
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("test"), fixtures.Image+":"+fixtures.V3, syncTimeout)

	_, rb := rbRollback(t, a, "test")
	actor := cliUser(t)
	e.WaitStepState(t, a.ns, pipelineName, rb, "test", "WaitingForMerge", promoteTimeout)
	head := "kardinal/" + rb + "/test"
	pr := e.WaitPR(t, a.repo, time.Minute, "the rollback PR", func(pr gitserver.PR) bool { return pr.Head == head })
	assert.Equal(t, "open", pr.State)
	assert.Equal(t, a.repo.Branch, pr.Base)
	assert.Equal(t, fmt.Sprintf("[kardinal] Rollback test to %s (restores %s)", rb, fixtures.V2), pr.Title)
	assert.ElementsMatch(t, []string{"kardinal", "kardinal/promotion", "kardinal/rollback"}, pr.Labels)
	assert.NotContains(t, pr1.Labels, "kardinal/rollback", "a forward promotion PR is not labelled a rollback")
	want := fmt.Sprintf(`<!-- kardinal-promoter auto-generated PR -->
## ROLLBACK: %[1]s -> podinfo/test

> **This is a rollback PR.** It restores the images of bundle %[2]s in environment test.
> Rolling back FROM: %[3]s (%[4]s)
> Rolling back TO: %[2]s (%[5]s)
> Rolled back by: %[6]s

### Artifact Provenance

| Image | Tag | Digest | CI Run | Commit SHA | Author |
|---|---|---|---|---|---|
| %[7]s | %[5]s | — | [CI run](https://ci.example/run/1) | 0123abc | e2e-bot |

### Policy Gate Compliance
`, rb, b1, b2, fixtures.V3, fixtures.V2, actor, fixtures.Image)
	assert.True(t, strings.HasPrefix(pr.Body, want), "rollback PR body:\n%s\nwant prefix:\n%s", pr.Body, want)

	assert.Contains(t, e.ReadFile(t, a.repo, head, kust), "newTag: "+fixtures.V2, "the PR branch restores the old version")
	assertEnvAt(t, a, "test", fixtures.V3)

	require.NoError(t, e.Git.MergePR(context.Background(), a.repo, pr.Number))
	ps := e.WaitStepState(t, a.ns, pipelineName, rb, "test", "Verified", promoteTimeout)
	assert.Equal(t, pr.URL, ps.Status.PRURL)
	e.WaitBundlePhase(t, a.ns, rb, "Verified", time.Minute)
	assertEnvAt(t, a, "test", fixtures.V2)

	rows := rbHistory(t, a)
	require.Len(t, rows, 3)
	for i, w := range [][]string{
		{rb, "rollback", "test", fmt.Sprintf("#%d", pr.Number)},
		{b2, "promote", "test", fmt.Sprintf("#%d", pr2.Number)},
		{b1, "promote", "test", fmt.Sprintf("#%d", pr1.Number)},
	} {
		assert.Equal(t, w, rbHistoryRow(rows[i]), "history row %d", i)
	}
}

// TestRollback_PolicyGates checks that a rollback Bundle passes through the
// environment's PolicyGates like any Bundle: a gate that blocks rollbacks
// holds it, with the gate's message, and the deprecated --emergency flag only
// warns. A break-glass override lets the rollback through.
//
// Covers RB-GATES-01.
func TestRollback_PolicyGates(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	const (
		expr = `!("kardinal.io/rollback" in bundle.labels)`
		msg  = "rollbacks need an override"
	)
	g := framework.Gate(a.ns, "no-rollbacks", "test", expr, recheck)
	g.Spec.Message = msg
	e.CreateGate(t, g)
	a.apply(t, a.pipeline(nil))

	b1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitGateReady(t, a.ns, b1, "test", "no-rollbacks", true, expr+" = true", gateTimeout)
	rbVerified(t, a, b1, "test")
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	rbVerified(t, a, b2, "test")

	out, rb := rbRollback(t, a, "test", "--emergency")
	assert.Equal(t, "Flag --emergency has been deprecated, it has no effect; use kardinal override to pass a blocking gate\n"+
		rbOutput("test", b2, b1, imageV2, rb), out)
	gate := e.WaitGateReady(t, a.ns, rb, "test", "no-rollbacks", false, expr+" = false", gateTimeout)
	assert.Equal(t, msg+" (bundle.version="+fixtures.V2+": "+expr+" = false)", gate.Status.Reason)
	e.NoStep(t, a.ns, pipelineName, rb, "test", holdFor)
	assertEnvAt(t, a, "test", fixtures.V3)

	ov := e.MustKardinal(t, a.ns, "override", pipelineName, "--stage", "test", "--gate", "no-rollbacks",
		"--reason", "rollback of a bad release", "--expires-in", "1h")
	assert.Contains(t, ov, fmt.Sprintf("Override applied: gate=%s pipeline=%s stage=test", gate.Name, pipelineName))
	m := createdByRE.FindStringSubmatch(ov)
	require.Len(t, m, 2, "override output names its author:\n%s", ov)
	e.WaitGateReady(t, a.ns, rb, "test", "no-rollbacks", true, "OVERRIDDEN by "+m[1]+": rollback of a bad release", gateTimeout)
	rbVerified(t, a, rb, "test")
	assertEnvAt(t, a, "test", fixtures.V2)
}

// TestRollback_History checks that kardinal history lists a rollback as
// such, with its duration, newest first, and that historyLimit bounds how
// far back a rollback can go: once more Bundles finished than the limit, the
// oldest is deleted, and rollback --to it fails as not found. The manual
// rollback it lists has no kardinal.io/reason label, passes the Argo CD
// health check like any promotion and gets a PromotionSucceeded AuditEvent,
// but no RollbackStarted one. rollback --to an unknown environment is
// refused. (TestCLI_Rollback covers the rollback itself.)
//
// Covers RB-HISTORY-01.
func TestRollback_History(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	p := a.pipeline(nil)
	p.Spec.HistoryLimit = 2
	a.apply(t, p)

	b1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	rbVerified(t, a, b1, "test")
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	rbVerified(t, a, b2, "test")
	assert.Equal(t, `rollback: pipeline podinfo has no environment "nope": invalid request`,
		rbRefused(t, a, "--env", "nope", "--to", b1))
	rbOnlyBundles(t, e, a.ns, b1, b2)

	_, rb := rbRollback(t, a, "test")
	rbAssertBundle(t, e, a.ns, rb, "test", b2, b1, cliUser(t), "")
	ps := e.WaitStepState(t, a.ns, pipelineName, rb, "test", "Verified", promoteTimeout)
	assert.Equal(t, argoVerified, ps.Status.Message)
	e.WaitBundlePhase(t, a.ns, rb, "Verified", time.Minute)
	assertEnvAt(t, a, "test", fixtures.V2)
	assert.Len(t, auditActions(t, e, a.ns, rb, "test", "PromotionSucceeded"), 1, "the rollback's promotion is audited like any other")
	rbNoAudit(t, e, a.ns, "RollbackStarted")

	rows := rbHistory(t, a)
	require.Len(t, rows, 3)
	for i, w := range [][]string{
		{rb, "rollback", "test", "--"},
		{b2, "promote", "test", "--"},
		{b1, "promote", "test", "--"},
	} {
		assert.Equal(t, w, rbHistoryRow(rows[i]), "history row %d", i)
		assert.Regexp(t, rbFinished, rows[i]["DURATION"], "history row %d: a finished step's duration", i)
	}

	// b1, b2 and rb are finished: a new Bundle makes three, one over the
	// limit, and the oldest, b1, is deleted.
	b3 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V1)
	framework.Eventually(t, time.Minute, "historyLimit deletes "+b1, func(ctx context.Context) (bool, string) {
		err := e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: b1}, &v1alpha1.Bundle{})
		return apierrors.IsNotFound(err), fmt.Sprintf("get %s: %v", b1, err)
	})
	assert.Equal(t, fmt.Sprintf("rollback: bundle %s/%s: not found", a.ns, b1), rbRefused(t, a, "--env", "test", "--to", b1))
	rbVerified(t, a, b3, "test")
	rbOnlyBundles(t, e, a.ns, b2, rb, b3)

	framework.Eventually(t, time.Minute, "history without the deleted Bundle", func(context.Context) (bool, string) {
		out, err := e.Kardinal(t, a.ns, "history", pipelineName)
		if err != nil {
			return false, fmt.Sprintf("kardinal history: %v: %s", err, out)
		}
		var got []string
		for _, r := range framework.ParseTable(out) {
			got = append(got, r["BUNDLE"]+"/"+r["ACTION"])
		}
		return assert.ObjectsAreEqual([]string{b3 + "/promote", rb + "/rollback", b2 + "/promote"}, got), strings.Join(got, " ")
	})
}

// rbPolicy creates a RollbackPolicy for the Bundle in test.
func rbPolicy(t *testing.T, a *app, bundle string, threshold int) *v1alpha1.RollbackPolicy {
	t.Helper()
	rp := &v1alpha1.RollbackPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "rp", Namespace: a.ns},
		Spec: v1alpha1.RollbackPolicySpec{PipelineName: pipelineName, Environment: "test", BundleRef: bundle,
			FailureThreshold: threshold},
	}
	require.NoError(t, a.e.Client.Create(context.Background(), rp))
	return rp
}

// rbWaitPolicy waits until the RollbackPolicy's status satisfies match.
func rbWaitPolicy(t *testing.T, a *app, what string, match func(v1alpha1.RollbackPolicyStatus) bool) *v1alpha1.RollbackPolicy {
	t.Helper()
	var rp v1alpha1.RollbackPolicy
	framework.Eventually(t, 2*time.Minute, "RollbackPolicy "+what, func(ctx context.Context) (bool, string) {
		if err := a.e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: "rp"}, &rp); err != nil {
			return false, err.Error()
		}
		return match(rp.Status), rbDescribePolicy(&rp)
	})
	return &rp
}

func rbDescribePolicy(rp *v1alpha1.RollbackPolicy) string {
	s := rp.Status
	name := "<nil>"
	if s.RollbackBundleName != nil {
		name = *s.RollbackBundleName
	}
	return fmt.Sprintf("shouldRollback=%v failures=%d rollbackBundle=%s lastEvaluated=%v conditions=%v",
		s.ShouldRollback, s.ConsecutiveFailures, name, s.LastEvaluatedAt, s.Conditions)
}

// TestRollback_Policy checks a RollbackPolicy with the default
// failureThreshold: it counts the Bundle's consecutive health failures in
// the environment without acting below 3, and at 3 creates one rollback
// Bundle to the previous Verified Bundle, records it, and sets
// RollbackRefused False. The rollback promotes, the old version runs, and the
// failing Bundle ends Superseded.
//
// Covers RB-POLICY-01.
func TestRollback_Policy(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	p := a.resourcePipeline(nil)
	// The bake keeps the second Bundle in HealthChecking while its pod turns
	// unready, so its health checks fail one by one.
	envSpec(t, p, "test").Bake = &v1alpha1.BakeConfig{Minutes: 1}
	a.apply(t, p)

	b1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	rbVerified(t, a, b1, "test")
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	waitBakeStarted(t, e, a, b2, 1)
	rbPolicy(t, a, b2, 0)
	rp := rbWaitPolicy(t, a, "evaluated with no failure", func(s v1alpha1.RollbackPolicyStatus) bool {
		return s.LastEvaluatedAt != nil
	})
	assert.Equal(t, 0, rp.Status.ConsecutiveFailures)
	assert.False(t, rp.Status.ShouldRollback)
	// failureThreshold is unset: the reconciler uses 3 for 0, and since B49
	// (#1386) the CRD defaults it to 3.
	assert.Contains(t, []int{0, 3}, rp.Spec.FailureThreshold, "failureThreshold is unset")

	e.SetReadyz(t, a.ns, map[string]string{"app.kubernetes.io/name": fixtures.Workload("test")}, false)
	rbWaitPolicy(t, a, "counting failures below the threshold", func(s v1alpha1.RollbackPolicyStatus) bool {
		return s.ConsecutiveFailures >= 1 && s.ConsecutiveFailures < 3 && !s.ShouldRollback && s.RollbackBundleName == nil
	})
	rp = rbWaitPolicy(t, a, "to create the rollback Bundle", func(s v1alpha1.RollbackPolicyStatus) bool {
		return s.RollbackBundleName != nil
	})
	rb := b2 + "-rollback-policy"
	assert.Equal(t, rb, *rp.Status.RollbackBundleName)
	assert.True(t, rp.Status.ShouldRollback)
	assert.Equal(t, 3, rp.Status.ConsecutiveFailures)
	c := meta.FindStatusCondition(rp.Status.Conditions, "RollbackRefused")
	require.NotNil(t, c, rbDescribePolicy(rp))
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, "RollbackCreated", c.Reason)
	assert.Equal(t, fmt.Sprintf("rollback Bundle %s was created", rb), c.Message)

	rbAssertBundle(t, e, a.ns, rb, "test", b2, b1, policyActor, "AutoRollback")
	rbVerified(t, a, rb, "test")
	assertEnvAt(t, a, "test", fixtures.V2)
	rbOnlyBundles(t, e, a.ns, b1, b2, rb)
	e.WaitBundlePhase(t, a.ns, b2, "Superseded", time.Minute)
}

// TestRollback_PolicyRefused checks a RollbackPolicy that reaches its
// threshold with nothing safe to roll back to: it creates nothing, sets
// RollbackRefused True with the reason, emits one Warning Event, and is not
// evaluated again while nothing changes.
//
// Covers RB-POLICY-02.
func TestRollback_PolicyRefused(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a, b1 := brokenRollout(t, e, "")
	ps := e.WaitStepState(t, a.ns, pipelineName, b1, "test", "Failed", promoteTimeout)
	require.Equal(t, 1, ps.Status.ConsecutiveHealthFailures)

	rbPolicy(t, a, b1, 1)
	rp := rbWaitPolicy(t, a, "to refuse", func(s v1alpha1.RollbackPolicyStatus) bool {
		return meta.IsStatusConditionTrue(s.Conditions, "RollbackRefused")
	})
	want := fmt.Sprintf("rollback: no earlier Bundle with artifacts, not already rolled back from, was Verified in test "+
		"(deployed now: %s): conflict", b1)
	c := meta.FindStatusCondition(rp.Status.Conditions, "RollbackRefused")
	assert.Equal(t, "NoSafeTarget", c.Reason)
	assert.Equal(t, want, c.Message)
	assert.True(t, rp.Status.ShouldRollback)
	assert.Nil(t, rp.Status.RollbackBundleName)
	assert.Equal(t, 1, rp.Status.ConsecutiveFailures)

	var events []eventsv1.Event
	framework.Eventually(t, time.Minute, "a RollbackRefused Event", func(ctx context.Context) (bool, string) {
		evs, err := e.Events(ctx, a.ns, "RollbackPolicy", "rp")
		if err != nil {
			return false, err.Error()
		}
		events = rbReason(evs, "RollbackRefused")
		return len(events) > 0, fmt.Sprintf("reasons %v", eventReasons(evs))
	})
	require.Len(t, events, 1)
	assert.Equal(t, corev1.EventTypeWarning, events[0].Type)
	assert.Equal(t, "Rollback", events[0].Action)
	assert.Equal(t, fmt.Sprintf("env test: no rollback Bundle created for %s after 1 consecutive health failure: %s", b1, want),
		events[0].Note)

	evaluated := rp.Status.LastEvaluatedAt
	require.NotNil(t, evaluated)
	framework.Consistently(t, 40*time.Second, "the refused policy is not evaluated again", func(ctx context.Context) (bool, string) {
		var cur v1alpha1.RollbackPolicy
		if err := e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: "rp"}, &cur); err != nil {
			return false, err.Error()
		}
		n, err := rbCountBundles(ctx, e, a.ns)
		if err != nil {
			return false, err.Error()
		}
		evs, err := e.Events(ctx, a.ns, "RollbackPolicy", "rp")
		if err != nil {
			return false, err.Error()
		}
		refused := rbReason(evs, "RollbackRefused")
		same := cur.Status.LastEvaluatedAt != nil && cur.Status.LastEvaluatedAt.Equal(evaluated)
		return same && n == 1 && len(refused) == 1 && refused[0].Series == nil,
			fmt.Sprintf("%s; %d Bundles; %d RollbackRefused Events", rbDescribePolicy(&cur), n, len(refused))
	})
}

// rbAlarmPipeline applies a resource-health Pipeline for test with
// onHealthFailure rollback and a health timeout longer than the broken
// rollout's progress deadline. bake, when set, is test's bake.
func rbAlarmPipeline(t *testing.T, e *framework.Env, bake *v1alpha1.BakeConfig) *app {
	t.Helper()
	a := newArgoApp(t, e, "test")
	p := a.resourcePipeline(nil)
	test := envSpec(t, p, "test")
	test.OnHealthFailure = "rollback"
	test.Health.Timeout = "5m"
	test.Bake = bake
	a.apply(t, p)
	return a
}

// TestRollback_OnHealthFailure checks onHealthFailure: rollback end to end: a
// release whose rollout fails stops at RollingBack with a message naming the
// rollback Bundle, which the controller created from the previous Verified
// Bundle. The step's RollbackStarted AuditEvent and Warning Event carry the
// message, kardinal get auditevents lists it, and the rollback promotes the
// old version back. The failed step stays RollingBack and its Bundle ends
// Superseded.
//
// Covers ONFAIL-ROLLBACK-01, RB-AUDIT-01.
func TestRollback_OnHealthFailure(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := rbAlarmPipeline(t, e, nil)

	b1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	rbVerified(t, a, b1, "test")
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
	ps := e.WaitStepState(t, a.ns, pipelineName, b2, "test", "RollingBack", promoteTimeout)
	rb := b2 + "-rollback-alarm"
	msg := ps.Status.Message
	assert.True(t, strings.HasPrefix(msg, fmt.Sprintf(
		"health alarm via resource (onHealthFailure=rollback): Deployment %s/%s rollout failed: ProgressDeadlineExceeded: ",
		a.ns, fixtures.Workload("test"))), msg)
	assert.True(t, strings.HasSuffix(msg, " — rollback Bundle "+rb+" created"), msg)

	rbAssertBundle(t, e, a.ns, rb, "test", b2, b1, alarmActor, "AutoRollback")
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}, getBundle(t, e, a.ns, rb).Spec.Images)

	var audit v1alpha1.AuditEvent
	framework.Eventually(t, time.Minute, "the RollbackStarted AuditEvent", func(ctx context.Context) (bool, string) {
		err := e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: ps.Name + "-rollback-started"}, &audit)
		return err == nil, fmt.Sprint(err)
	})
	assert.Equal(t, v1alpha1.AuditEventSpec{Timestamp: audit.Spec.Timestamp, BundleName: b2, PipelineName: pipelineName,
		Environment: "test", Action: "RollbackStarted", Outcome: "Pending", Message: msg}, audit.Spec)
	assert.Equal(t, map[string]string{"kardinal.io/pipeline": pipelineName, "kardinal.io/bundle": b2,
		"kardinal.io/environment": "test", "kardinal.io/action": "RollbackStarted"}, audit.Labels)

	var events []eventsv1.Event
	framework.Eventually(t, time.Minute, "the RollingBack Event", func(ctx context.Context) (bool, string) {
		evs, err := e.Events(ctx, a.ns, "PromotionStep", ps.Name)
		if err != nil {
			return false, err.Error()
		}
		events = rbReason(evs, "RollingBack")
		return len(events) > 0, fmt.Sprintf("reasons %v", eventReasons(evs))
	})
	require.Len(t, events, 1)
	assert.Equal(t, corev1.EventTypeWarning, events[0].Type)
	assert.Equal(t, "Rollback", events[0].Action)
	assert.Equal(t, "env test: "+msg, events[0].Note)

	out := e.MustKardinal(t, a.ns, "get", "auditevents", "--pipeline", pipelineName, "--bundle", b2, "--env", "test")
	var actions []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		f := strings.Fields(l)
		require.GreaterOrEqual(t, len(f), 6, "auditevents row %q", l)
		require.Equal(t, []string{pipelineName, b2, "test"}, f[1:4], "auditevents row %q", l)
		actions = append(actions, f[4]+"/"+f[5])
	}
	assert.Contains(t, actions, "RollbackStarted/Pending", "kardinal get auditevents:\n%s", out)

	rbVerified(t, a, rb, "test")
	assertEnvAt(t, a, "test", fixtures.V2)
	assert.Equal(t, "RollingBack", e.MustStep(t, a.ns, pipelineName, b2, "test").Status.State,
		"the failed step stays RollingBack; the rollback Bundle carries on")
	assert.Empty(t, auditActions(t, e, a.ns, rb, "test", "RollbackStarted"), "the rollback Bundle's own promotion is not a RollbackStarted")
	e.WaitBundlePhase(t, a.ns, b2, "Superseded", time.Minute)
}

// TestRollback_OnHealthFailureEarlierDeadline checks that the rollback
// Bundle's step does not fail on the ProgressDeadlineExceeded the broken
// release left (B92). The rollback goes back to the ReplicaSet the Deployment
// ran before, so the Deployment controller creates no ReplicaSet: it observes
// the rolled-back template and keeps the broken rollout's condition until it
// sees the broken pod go. The test holds that pod (the API server denies its
// delete) to keep the condition for as long as it checks: the step waits,
// without counting a failure, and is Verified once the pod goes.
//
// Covers HEALTH-RES-07.
func TestRollback_OnHealthFailureEarlierDeadline(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := rbAlarmPipeline(t, e, nil)
	broken := fixtures.Image + ":" + fixtures.BrokenTag
	workload := fixtures.Workload("test")

	b1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	rbVerified(t, a, b1, "test")
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", broken)
	lift := e.HoldPodDeletion(t, a.ns, broken, promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, b2, "test", "RollingBack", promoteTimeout)
	rb := b2 + "-rollback-alarm"

	earlier := fmt.Sprintf("Deployment %s/%s: ProgressDeadlineExceeded (", a.ns, workload)
	ps := e.WaitStepMessageAll(t, a.ns, pipelineName, rb, "test", "HealthChecking", promoteTimeout,
		earlier, ") is from an earlier rollout: its lastUpdateTime ", " is before this health check started (",
		"; waiting for the Deployment controller to see this rollout progress")
	assert.Zero(t, ps.Status.ConsecutiveHealthFailures, framework.DescribeStep(ps))
	assert.NotNil(t, ps.Status.TargetUpdatedAt, "status.targetUpdatedAt: the check found the template on the Bundle images")

	var d appsv1.Deployment
	require.NoError(t, e.Client.Get(context.Background(), client.ObjectKey{Namespace: a.ns, Name: workload}, &d))
	assert.Equal(t, imageV2, d.Spec.Template.Spec.Containers[0].Image, "the rolled-back template")
	assert.Equal(t, d.Generation, d.Status.ObservedGeneration, "the Deployment controller observed it")
	var prog *appsv1.DeploymentCondition
	for i := range d.Status.Conditions {
		if d.Status.Conditions[i].Type == appsv1.DeploymentProgressing {
			prog = &d.Status.Conditions[i]
		}
	}
	if assert.NotNil(t, prog, "Progressing condition") {
		assert.Equal(t, "ProgressDeadlineExceeded", prog.Reason, "the broken rollout's condition stays: %s", prog.Message)
	}

	e.HoldStep(t, deliveryHold, a.ns, pipelineName, rb, "test", "the rollback waits on the earlier deadline",
		func(ps *v1alpha1.PromotionStep) bool {
			return ps.Status.State == "HealthChecking" && ps.Status.ConsecutiveHealthFailures == 0 &&
				strings.Contains(ps.Status.Message, earlier)
		})
	lift()
	rbVerified(t, a, rb, "test")
	assertEnvAt(t, a, "test", fixtures.V2)
}

// TestRollback_OnHealthFailureRefused checks the two cases where
// onHealthFailure: rollback does not roll back and stops the step at
// AbortedByAlarm for a human: a first release with no earlier Verified
// Bundle, and a rollback Bundle that fails its own health check (here during
// a fail-on-alarm bake), which is not rolled back again. Neither creates a
// Bundle.
//
// Covers ONFAIL-ROLLBACK-02.
func TestRollback_OnHealthFailureRefused(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := rbAlarmPipeline(t, e, &v1alpha1.BakeConfig{Minutes: 1, Policy: "fail-on-alarm"})

	b0 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
	ps := e.WaitStepState(t, a.ns, pipelineName, b0, "test", "AbortedByAlarm", promoteTimeout)
	assert.True(t, strings.HasPrefix(ps.Status.Message, fmt.Sprintf(
		"health alarm via resource (onHealthFailure=rollback): Deployment %s/%s rollout failed: ProgressDeadlineExceeded: ",
		a.ns, fixtures.Workload("test"))), ps.Status.Message)
	assert.True(t, strings.HasSuffix(ps.Status.Message, fmt.Sprintf(" — no automatic rollback (rollback: no earlier Bundle "+
		"with artifacts, not already rolled back from, was Verified in test (deployed now: %s): conflict); "+
		"human intervention required", b0)), ps.Status.Message)
	e.WaitBundlePhase(t, a.ns, b0, "Failed", time.Minute)
	rbOnlyBundles(t, e, a.ns, b0)

	b1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	rbVerified(t, a, b1, "test")
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	rbVerified(t, a, b2, "test")

	out, rb := rbRollback(t, a, "test")
	assert.Equal(t, rbOutput("test", b2, b1, imageV2, rb), out)
	waitBakeStarted(t, e, a, rb, 1)
	e.SetReadyz(t, a.ns, map[string]string{"app.kubernetes.io/name": fixtures.Workload("test")}, false)
	ps = e.WaitStepState(t, a.ns, pipelineName, rb, "test", "AbortedByAlarm", time.Minute)
	assert.True(t, strings.HasPrefix(ps.Status.Message, fmt.Sprintf(
		"health alarm via resource (onHealthFailure=rollback): Deployment %s/%s: 0 of 1 updated replicas available (Available=False",
		a.ns, fixtures.Workload("test"))), ps.Status.Message)
	assert.True(t, strings.HasSuffix(ps.Status.Message, fmt.Sprintf(
		" — Bundle %s is a rollback and is not rolled back again; human intervention required", rb)), ps.Status.Message)
	e.WaitBundlePhase(t, a.ns, rb, "Failed", time.Minute)

	framework.Consistently(t, 20*time.Second, "no rollback of the rollback", func(ctx context.Context) (bool, string) {
		n, err := rbCountBundles(ctx, e, a.ns)
		if err != nil {
			return false, err.Error()
		}
		ps, _, err := e.Step(ctx, a.ns, pipelineName, rb, "test")
		if err != nil || ps == nil {
			return false, fmt.Sprintf("step lookup: %v", err)
		}
		return n == 4 && ps.Status.State == "AbortedByAlarm", fmt.Sprintf("%d Bundles; %s", n, framework.DescribeStep(ps))
	})
	rbNoAudit(t, e, a.ns, "RollbackStarted")
}

// TestRollback_MultiEnvironment checks a rollback of one of two environments
// that both follow test: the rollback Bundle goes through test first, then
// the target, and leaves the other environment, not upstream of the target,
// on the new version.
//
// Covers RB-MULTI-01.
func TestRollback_MultiEnvironment(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod-us", "prod-eu")
	p := a.pipeline(nil)
	envSpec(t, p, "prod-us").DependsOn = []string{"test"}
	envSpec(t, p, "prod-eu").DependsOn = []string{"test"}
	a.apply(t, p)
	all := []string{"test", "prod-us", "prod-eu"}

	b1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	rbVerified(t, a, b1, all...)
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	rbVerified(t, a, b2, all...)

	out, rb := rbRollback(t, a, "prod-us")
	assert.Equal(t, rbOutput("prod-us", b2, b1, imageV2, rb), out)
	rbAssertBundle(t, e, a.ns, rb, "prod-us", b2, b1, cliUser(t), "")
	test := e.WaitStepState(t, a.ns, pipelineName, rb, "test", "Verified", promoteTimeout)
	prodUS := e.WaitStepState(t, a.ns, pipelineName, rb, "prod-us", "Verified", promoteTimeout)
	at, _ := verifiedCondition(t, test)
	assert.False(t, prodUS.CreationTimestamp.Time.Before(at.Truncate(time.Second)),
		"prod-us starts after test is Verified (test %s, prod-us created %s)", at, prodUS.CreationTimestamp)
	e.WaitBundlePhase(t, a.ns, rb, "Verified", time.Minute)
	assertEnvAt(t, a, "test", fixtures.V2)
	assertEnvAt(t, a, "prod-us", fixtures.V2)

	e.NoStep(t, a.ns, pipelineName, rb, "prod-eu", holdFor)
	assertEnvAt(t, a, "prod-eu", fixtures.V3)
}
