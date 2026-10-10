//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The GitHub tests run in the github suite, whose controller runs with
// --scm-provider=github against github.com. Each test works on its own
// branch of one shared repo (hack/e2e/components/github.sh). github.com
// can't reach the kind cluster, so no test repo has a webhook: the webhook
// tests post the event GitHub would send, signed with the suite's secret.
//
// A refused label request needs a token that may open PRs but not label
// them, and an approval a second account: a GitHub user can't approve their
// own PR. The suite has one token, so those are contract rows, SCM-GH-11
// (pkg/reconciler/promotionstep) and SCM-GH-12 (pkg/reconciler/prstatus),
// against a fake GitHub API.

// TestGitHub_PromotionPR checks the PR a pr-review environment opens on
// GitHub, and that rerunning open-pr finds it instead of opening another.
//
// Covers SCM-GH-01, SCM-GH-08.
func TestGitHub_PromotionPR(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "github")
	assert.Contains(t, controllerArgs(t, e), "--scm-provider=github")
	scmPromotionPR(t, e, nil)
}

// TestGitHub_Labels checks the labels kardinal puts on GitHub PRs: kardinal
// and kardinal/promotion, plus kardinal/rollback on the PR of a `kardinal
// rollback`.
//
// Covers SCM-GH-02.
func TestGitHub_Labels(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "github")
	scmLabels(t, e)
}

// TestGitHub_UnreviewedPR checks that the PRStatus poll reads the reviews of
// a GitHub PR no one reviewed: not approved, no approvals, and no failed
// review read, and a gate on bundle.pr["staging"].isApproved holds prod.
//
// Covers SCM-GH-07.
func TestGitHub_UnreviewedPR(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "github")
	scmUnreviewed(t, e)
}

// TestGitHub_MergeByPolling checks that without a webhook the PRStatus poll
// finds a merge on GitHub.
//
// Covers SCM-GH-03.
func TestGitHub_MergeByPolling(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "github")
	scmMergeByPolling(t, e, nil)
}

// TestGitHub_MergeWebhook checks a merge event signed in
// X-Hub-Signature-256: it marks the PRStatus merged before a poll would,
// with the merge commit it carries.
//
// Covers SCM-GH-04.
func TestGitHub_MergeWebhook(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "github")
	a := newArgoAppIn(t, e, e.RepoWithoutWebhook, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	a.mergeByWebhook(t, bundle, "prod", hubSignature, true)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGitHub_WebhookBadSignature checks that a merge event with a wrong or
// no X-Hub-Signature-256, or with the right HMAC but without its "sha256="
// prefix, is refused and changes nothing.
//
// Covers SCM-GH-05.
func TestGitHub_WebhookBadSignature(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "github")
	a, bundle, pr, prs := scmBadSignature(t, e, hubSignature)
	body := mergedEvent(t, "github", prs.Spec.Repo, pr.Number, pr.HeadSHA)
	bare := map[string]string{hubSignature: framework.HMACHex(framework.WebhookSecret(t), body), eventHeader(hubSignature): "pull_request"}
	assert.Equal(t, http.StatusUnauthorized, e.PostSCMWebhook(t, bare, body), "the right HMAC without sha256=")
	a.stillOpen(t, bundle, "prod", 10*time.Second, "after the unprefixed signature")
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
}

// TestGitHub_ClosesPRs checks that a newer Bundle and waitForMergeTimeout
// each close the open GitHub PR with a comment, delete its head branch, and
// fail the step, and that the closed PR cannot be merged: GitHub's merge API
// merges a closed, unmerged PR, but not once its head branch is gone.
//
// Covers SCM-GH-06.
func TestGitHub_ClosesPRs(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "github")
	scmClosesPRs(t, e)
}

// TestGitHub_TokenCheck checks that a token GitHub rejects gets the startup
// scope warning and the controller still serves.
//
// Covers SCM-GH-09.
func TestGitHub_TokenCheck(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "github")
	scmRejectedToken(t, e, e.Namespace(t))
}

// TestGitHub_SCMAPIURL checks that --scm-api-url is the API base the
// controller calls, as for a GitHub Enterprise server. The PR is opened on
// github.com, the default API. Then the flag is pointed at a bucket of the
// suite's webhook receiver, which records every request and answers 200
// "OK": the startup token check asks it for /user and the PRStatus poll for
// /repos/<repo>/pulls/<n>, each with the token as a Bearer and GitHub's
// Accept header. The answer has no X-OAuth-Scopes header, so the check
// reports the scopes unverified; "OK" is not GitHub's JSON, so the poll
// fails to decode it and retries, and the step keeps waiting. Back
// on the suite's controller, the merge is seen and the step is Verified.
// The PR is opened before the host changes, so no branch is pushed without
// a PR that cleanup closes. Not parallel: it changes the controller's flags.
//
// Covers SCM-GH-10.
func TestGitHub_SCMAPIURL(t *testing.T) {
	e := framework.New(t)
	requireKind(t, e, "github")
	rec := framework.NewReceiver(t)
	a := newArgoApp(t, e, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps, pr := a.waitOpenPR(t, bundle, "prod")
	prs := e.WaitPRStatus(t, a.ns, pipelineName, bundle, "prod", time.Minute, "tracking the open PR",
		func(p *v1alpha1.PRStatus) bool { return p.Spec.PRNumber == pr.Number && p.Status.Open })

	api := strings.TrimRight(rec.URL(a.ns, ""), "/")
	since := time.Now()
	restore := e.PatchController(t, func(spec *corev1.PodSpec) { framework.SetArg(spec, "scm-api-url", api) })
	assert.Contains(t, controllerArgs(t, e), "--scm-api-url="+api)
	userPath := "/" + a.ns + "/user"
	pullPath := fmt.Sprintf("/%s/repos/%s/pulls/%d", a.ns, prs.Spec.Repo, pr.Number)
	got := map[string]framework.Received{}
	framework.Eventually(t, 90*time.Second, "the receiver to get the token check and the PRStatus poll", func(ctx context.Context) (bool, string) {
		recs, err := rec.Records(ctx, a.ns)
		if err != nil {
			return false, err.Error()
		}
		var paths []string
		for _, r := range recs {
			paths = append(paths, r.Method+" "+r.Path)
			if r.Method == http.MethodGet && (r.Path == userPath || r.Path == pullPath) {
				got[r.Path] = r
			}
		}
		return len(got) == 2, fmt.Sprintf("%d requests: %s", len(recs), strings.Join(paths, ", "))
	})
	for path, r := range got {
		// The header's value is the token; it is not logged.
		assert.True(t, r.Header("Authorization") == "Bearer "+os.Getenv(gitserver.EnvToken), "%s: the suite's token goes as a Bearer", path)
		assert.Equal(t, "application/vnd.github+json", r.Header("Accept"), path)
	}
	e.WaitControllerLog(t, since, time.Minute, "the token check to read the receiver's answer",
		framework.LogMessage("SCM TOKEN SCOPE WARNING", "provider", "github", "missing_scope", "<unverified>"))
	l := e.WaitControllerLog(t, since, 90*time.Second, "the PRStatus poll to fail on the receiver's answer",
		framework.LogMessage("GetPRStatus failed, will retry", "prstatus", ps.Spec.PRStatusRef, "namespace", a.ns))
	assert.Contains(t, l.Str("error"), "decode response")
	a.stillOpen(t, bundle, "prod", 10*time.Second, "while the API host is the receiver")

	restore()
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGitHub_ExampleGitHubDemo runs examples/github-demo on GitHub: its
// Pipeline and its three team PolicyGates, then Bundles as CI would create
// them.
//
// A good Bundle bakes through uat; at prod uat-soak-gate blocks until uat has
// soaked, no-weekend-deploys passes on a weekday (an operator overrides it on
// a weekend) and no-bot-deploys passes. prod opens a PR titled and labeled as
// the README says, whose body has the provenance, gate and upstream sections;
// after the merge prod bakes and is Verified. A second Bundle whose prod pods
// turn unready during the prod bake is never healthy again, so at
// health.timeout onHealthFailure: rollback creates <bundle>-rollback-alarm
// from the good Bundle. The rollback goes through test and uat, and its prod
// PR carries kardinal/rollback and the rollback note naming both Bundles and
// the controller as the actor. Once merged, every environment runs the good
// release again.
//
// Changes to the example: the namespace, Git URL and branch; the Argo CD
// Application names (the test's, since the quickstart test's
// kardinal-test-app-<env> may run alongside); the gates are in the
// Pipeline's namespace; the bakes are shortened to 1m (uat) and 2m (prod,
// room to break the release mid-bake), uat-soak-gate to one minute and the
// prod health.timeout to 3m; the image is podinfo.
//
// Covers EX-GITHUB-DEMO-01.
func TestGitHub_ExampleGitHubDemo(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "github")
	ctx := context.Background()
	const name = "github-demo"
	a := newArgoApp(t, e, "test", "uat", "prod")
	ns := a.ns
	createExampleToken(t, e, ns)

	var pipeline *unstructured.Unstructured
	var gateNames []string
	msgs := map[string]string{}
	for _, obj := range exampleManifests(t, "github-demo/pipeline.yaml") {
		if obj.GetKind() == "Pipeline" {
			pipeline = obj
			continue
		}
		require.Equal(t, "PolicyGate", obj.GetKind())
		assert.Equal(t, "team", obj.GetLabels()["kardinal.io/scope"], obj.GetName())
		assert.Equal(t, "prod", obj.GetLabels()["kardinal.io/applies-to"], obj.GetName())
		obj.SetNamespace(ns)
		if obj.GetName() == "uat-soak-gate" {
			expr, _, _ := unstructured.NestedString(obj.Object, "spec", "expression")
			require.Equal(t, "upstream.uat.soakMinutes >= 30", expr)
			require.NoError(t, unstructured.SetNestedField(obj.Object, "upstream.uat.soakMinutes >= 1", "spec", "expression"))
		}
		gateNames = append(gateNames, obj.GetName())
		msgs[obj.GetName()], _, _ = unstructured.NestedString(obj.Object, "spec", "message")
		_, err := e.Apply(ctx, obj)
		require.NoError(t, err, "apply PolicyGate %s", obj.GetName())
	}
	require.NotNil(t, pipeline, "the example has a Pipeline")
	require.ElementsMatch(t, []string{"no-weekend-deploys", "uat-soak-gate", "no-bot-deploys"}, gateNames)

	p := applyExamplePipeline(t, e, pipeline, ns, a.repo, func(u *unstructured.Unstructured) {
		envs, _, err := unstructured.NestedSlice(u.Object, "spec", "environments")
		require.NoError(t, err)
		bakes := map[string]int64{"uat": 1, "prod": 2}
		field := func(m map[string]interface{}, path ...string) string {
			v, _, _ := unstructured.NestedString(m, path...)
			return v
		}
		for _, x := range envs {
			env := x.(map[string]interface{})
			n := env["name"].(string)
			require.Equal(t, fixtures.Path(n), env["path"], "%s's path", n)
			require.Equal(t, "kardinal-test-app-"+n, field(env, "health", "argocd", "name"), "%s's Application", n)
			require.NoError(t, unstructured.SetNestedField(env, a.argoApp(n), "health", "argocd", "name"))
			if bake, ok := env["bake"].(map[string]interface{}); ok {
				require.Contains(t, bakes, n, "%s bakes", n)
				require.Equal(t, "fail-on-alarm", bake["policy"])
				bake["minutes"] = bakes[n]
			}
			if n == "prod" {
				require.Equal(t, "15m", field(env, "health", "timeout"))
				require.NoError(t, unstructured.SetNestedField(env, "3m", "health", "timeout"))
			}
		}
		require.NoError(t, unstructured.SetNestedSlice(u.Object, envs, "spec", "environments"))
	})
	prod := p.Spec.Environments[2]
	require.Equal(t, "prod", prod.Name)
	require.Equal(t, "pr-review", prod.Approval)
	require.Equal(t, "rollback", prod.OnHealthFailure)
	require.NotNil(t, prod.Bake)
	require.Equal(t, 2, prod.Bake.Minutes)

	// gates waits until bundle's prod gates pass: on a weekend an operator
	// overrides no-weekend-deploys first.
	gates := func(bundle string) {
		t.Helper()
		if weekendGate(t, e, ns, bundle, msgs["no-weekend-deploys"]) {
			e.MustKardinal(t, ns, "override", name, "--stage", "prod", "--gate", "no-weekend-deploys",
				"--reason", "e2e: the suite runs on weekends")
			e.WaitGateReady(t, ns, bundle, "prod", "no-weekend-deploys", true, "", gateTimeout)
		}
		e.WaitGateReady(t, ns, bundle, "prod", "no-bot-deploys", true, "= true", gateTimeout)
		e.WaitGateReady(t, ns, bundle, "prod", "uat-soak-gate", true, "= true", 3*time.Minute)
	}
	baked := func(bundle, env string, minutes int) {
		t.Helper()
		ps := e.WaitStepState(t, ns, name, bundle, env, "Verified", promoteTimeout)
		assert.Equal(t, fmt.Sprintf("bake complete: %dm contiguous healthy via argocd (resets=0)", minutes), ps.Status.Message)
	}

	good := e.CreateBundle(t, ns, name, "--image", imageV2)
	e.WaitStepState(t, ns, name, good, "test", "Verified", promoteTimeout)
	baked(good, "uat", 1)
	soak := e.WaitGate(t, ns, good, "prod", "uat-soak-gate", gateTimeout, "blocked until uat has soaked",
		func(g *v1alpha1.PolicyGate) bool {
			return g.Status.LastEvaluatedAt != nil && !g.Status.Ready && strings.HasPrefix(g.Status.Reason, msgs["uat-soak-gate"]+" (")
		})
	assert.Contains(t, soak.Status.Reason, "upstream.uat.soakMinutes >= 1 = false")
	gates(good)
	e.WaitStepState(t, ns, name, good, "prod", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, good, "prod")
	assert.Equal(t, "[kardinal] Promote "+good+" to prod", pr.Title)
	assert.ElementsMatch(t, []string{"kardinal", "kardinal/promotion"}, pr.Labels)
	assert.True(t, strings.HasPrefix(pr.Body, "<!-- kardinal-promoter auto-generated PR -->\n## Promotion: "+good+" -> "+name+"/prod\n"), pr.Body)
	for _, want := range []string{
		"### Artifact Provenance", "| " + fixtures.Image + " | " + fixtures.V2 + " |",
		"### Policy Gate Compliance", "| no-weekend-deploys | " + ns + " | Pass |", "| uat-soak-gate | " + ns + " | Pass |",
		"| no-bot-deploys | " + ns + " | Pass |",
		"### Upstream Verification", "| test | ", "| uat | ",
	} {
		assert.Contains(t, pr.Body, want)
	}
	assert.Equal(t, imageV1, e.DeploymentImage(t, ns, fixtures.Workload("prod")), "prod waits for the merge")
	a.merge(t, pr)
	baked(good, "prod", 2)
	e.WaitBundlePhase(t, ns, good, "Verified", time.Minute)

	// The next release passes prod's health check, then its pods turn
	// unready during the bake: the window stops and never restarts.
	bad := e.CreateBundle(t, ns, name, "--image", imageV3)
	e.WaitStepState(t, ns, name, bad, "test", "Verified", promoteTimeout)
	baked(bad, "uat", 1)
	gates(bad)
	e.WaitStepState(t, ns, name, bad, "prod", "WaitingForMerge", promoteTimeout)
	a.merge(t, a.openPR(t, bad, "prod"))
	e.WaitStep(t, ns, name, bad, "prod", promoteTimeout, "the prod bake to start", func(ps *v1alpha1.PromotionStep) (bool, string) {
		return ps.Status.BakeStartedAt != nil, framework.DescribeStep(ps)
	})
	e.FailReadiness(t, ns, fixtures.Workload("prod"))
	rb := bad + "-rollback-alarm"
	ps := e.WaitStepState(t, ns, name, bad, "prod", "RollingBack", promoteTimeout)
	assert.True(t, strings.HasPrefix(ps.Status.Message,
		"health alarm via argocd (onHealthFailure=rollback): health check timeout after 3m0s; last result: bake: "), ps.Status.Message)
	assert.True(t, strings.HasSuffix(ps.Status.Message, " — rollback Bundle "+rb+" created"), ps.Status.Message)
	e.WaitBundlePhase(t, ns, bad, "Superseded", time.Minute)

	b := getBundle(t, e, ns, rb)
	assert.Equal(t, "true", b.Labels["kardinal.io/rollback"])
	assert.Equal(t, "AutoRollback", b.Labels["kardinal.io/reason"])
	assert.Equal(t, name, b.Labels["kardinal.io/pipeline"])
	assert.Equal(t, bad, b.Annotations["kardinal.io/rollback-from"])
	assert.Equal(t, alarmActor, b.Annotations["kardinal.io/requested-by"])
	require.NotNil(t, b.Spec.Provenance)
	assert.Equal(t, good, b.Spec.Provenance.RollbackOf, "the rollback restores the last Bundle Verified in prod")
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}, b.Spec.Images)

	// The rollback is a forward promotion: test, uat and its soak, then a
	// prod PR.
	e.WaitStepState(t, ns, name, rb, "test", "Verified", promoteTimeout)
	baked(rb, "uat", 1)
	gates(rb)
	e.WaitStepState(t, ns, name, rb, "prod", "WaitingForMerge", promoteTimeout)
	pr = a.openPR(t, rb, "prod")
	assert.Equal(t, "[kardinal] Rollback prod to "+rb+" (restores "+fixtures.V2+")", pr.Title)
	assert.ElementsMatch(t, []string{"kardinal", "kardinal/promotion", "kardinal/rollback"}, pr.Labels)
	note := fmt.Sprintf("<!-- kardinal-promoter auto-generated PR -->\n## ROLLBACK: %s -> %s/prod\n\n"+
		"> **This is a rollback PR.** It restores the images of bundle %s in environment prod.\n"+
		"> Rolling back FROM: %s (%s)\n> Rolling back TO: %s (%s)\n> Rolled back by: %s\n",
		rb, name, good, bad, fixtures.V3, good, fixtures.V2, lifecycle.ControllerCreator)
	assert.True(t, strings.HasPrefix(pr.Body, note), "the rollback note:\n%s", pr.Body)
	assert.Contains(t, pr.Body, "### Policy Gate Compliance")
	a.merge(t, pr)
	baked(rb, "prod", 2)
	e.WaitBundlePhase(t, ns, rb, "Verified", time.Minute)
	for _, env := range a.envs {
		assertEnvAt(t, a, env, fixtures.V2)
	}
}
