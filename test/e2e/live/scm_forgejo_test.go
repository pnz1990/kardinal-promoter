//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The Forgejo tests run in the core suite, whose git server is Forgejo. The
// controller-wide settings (webhook secret, static token, API URL, circuit
// breaker) are tested here only, because they do not depend on the provider.

// TestForgejo_PromotionPR checks the PR a pr-review environment opens on
// Forgejo: title, head and base, labels and the evidence body with the gate
// and upstream tables. Rerunning open-pr finds that PR instead of opening a
// second one. The Pipeline sets the deprecated spec.git.provider to gitlab;
// the controller ignores it and uses its --scm-provider=forgejo.
//
// Covers SCM-FJ-01, SCM-FJ-08, SCM-PROVIDERFLAG-01, DEP-GITPROVIDER-01.
func TestForgejo_PromotionPR(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	assert.Contains(t, controllerArgs(t, e), "--scm-provider=forgejo")
	scmPromotionPR(t, e, func(p *v1alpha1.Pipeline) {
		p.Spec.Git.Provider = "gitlab" //nolint:staticcheck // SA1019: the deprecated field is what this test checks
	})
}

// TestForgejo_MergeByPolling checks that without a webhook the PRStatus poll
// finds a merge on Forgejo, and the Graph's PRStatus nodes: one per
// environment, named for the Bundle and environment, without a spec, ready
// when merged only for pr-review, and referenced by the step.
//
// Covers SCM-FJ-03, GRAPH-PRSTATUS-01.
func TestForgejo_MergeByPolling(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	scmMergeByPolling(t, e, func(a *app, bundle string, ps *v1alpha1.PromotionStep, pr gitserver.PR) {
		assertPRStatusNodes(t, a, bundle, ps, pr)
	})
}

// assertPRStatusNodes checks the PRStatus nodes of bundle's Graph and the
// PRStatus objects they made while prod's PR pr is open. prod is the only
// pr-review environment.
func assertPRStatusNodes(t *testing.T, a *app, bundle string, prod *v1alpha1.PromotionStep, pr gitserver.PR) {
	t.Helper()
	ctx := context.Background()
	name := a.bundle(t, bundle).Status.GraphRef
	require.NotEmpty(t, name, "Bundle %s names its Graph", bundle)
	g, err := a.e.Dynamic.Resource(framework.GraphGVR).Namespace(a.ns).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	nodes, _, err := unstructured.NestedSlice(g.Object, "spec", "nodes")
	require.NoError(t, err)
	prNodes := map[string]map[string]interface{}{}
	stepRefs := map[string]string{}
	for _, raw := range nodes {
		n, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		switch kind, _, _ := unstructured.NestedString(n, "template", "kind"); kind {
		case "PRStatus":
			env, _, _ := unstructured.NestedString(n, "template", "metadata", "labels", "kardinal.io/environment")
			prNodes[env] = n
		case "PromotionStep":
			env, _, _ := unstructured.NestedString(n, "template", "spec", "environment")
			stepRefs[env], _, _ = unstructured.NestedString(n, "template", "spec", "prStatusRef")
		}
	}
	require.Len(t, prNodes, len(a.envs), "one PRStatus node per environment")
	for _, env := range a.envs {
		n := prNodes[env]
		require.NotNil(t, n, "PRStatus node for %s", env)
		id, _, _ := unstructured.NestedString(n, "id")
		assert.True(t, strings.HasPrefix(id, "prstatus0") && strings.HasSuffix(id, "0"+env), "node id %q", id)
		k8sName, _, _ := unstructured.NestedString(n, "template", "metadata", "name")
		assert.Equal(t, "prstatus-"+bundle+"-"+env, k8sName)
		labels, _, _ := unstructured.NestedStringMap(n, "template", "metadata", "labels")
		assert.Equal(t, map[string]string{"kardinal.io/pipeline": pipelineName, "kardinal.io/bundle": bundle,
			"kardinal.io/environment": env}, labels)
		_, hasSpec := n["template"].(map[string]interface{})["spec"]
		assert.False(t, hasSpec, "the open-pr step owns the PRStatus spec, not kro")
		ready, _, _ := unstructured.NestedStringSlice(n, "readyWhen")
		if env == "prod" {
			assert.Equal(t, []string{"${" + id + ".status.merged == true}"}, ready, "a pr-review node is ready when merged")
		} else {
			assert.Empty(t, ready, "an auto environment opens no PR")
		}
		assert.Equal(t, "${"+id+".metadata.name}", stepRefs[env], "%s step's prStatusRef", env)
	}
	assert.Equal(t, "prstatus-"+bundle+"-prod", prod.Spec.PRStatusRef)

	var test v1alpha1.PRStatus
	require.NoError(t, a.e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: "prstatus-" + bundle + "-test"}, &test))
	assert.Zero(t, test.Spec.PRNumber, "an auto environment's PRStatus stays a placeholder")
	assert.False(t, test.Status.Merged)
	prs, err := a.e.PRStatusOf(ctx, prod)
	require.NoError(t, err)
	assert.Equal(t, pr.Number, prs.Spec.PRNumber)
	assert.True(t, strings.HasSuffix(prs.Spec.PRURL, "/"+strconv.Itoa(pr.Number)), "prURL %s", prs.Spec.PRURL)
	assert.True(t, prs.Status.Open && !prs.Status.Merged, framework.DescribePRStatus(prs))
}

// TestForgejo_MergeWebhook checks merge events signed in X-Gitea-Signature
// (uat) and X-Forgejo-Signature (prod), as Forgejo sends them: each marks
// the PRStatus merged before a poll would, and the reconciler fetches the
// merge commit, which Forgejo's event does not carry.
//
// Covers SCM-FJ-04.
func TestForgejo_MergeWebhook(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	a := newArgoAppIn(t, e, e.RepoWithoutWebhook, "uat", "prod")
	a.apply(t, a.pipeline(map[string]string{"uat": "pr-review", "prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	a.mergeByWebhook(t, bundle, "uat", giteaSignature, false)
	a.mergeByWebhook(t, bundle, "prod", forgejoSignature, false)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestForgejo_WebhookSignatures checks that a merge event with a wrong or no
// X-Forgejo-Signature or X-Gitea-Signature is refused and changes nothing,
// and that X-Hub-Signature-256, which Forgejo also sends, is accepted and
// decides alone: a wrong one is refused next to a valid X-Forgejo-Signature,
// a valid one is accepted next to a wrong X-Forgejo-Signature and marks the
// merged PR.
//
// Covers SCM-FJ-05, SCM-FJ-11.
func TestForgejo_WebhookSignatures(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	a, bundle, pr, prs := scmBadSignature(t, e, forgejoSignature, giteaSignature)
	secret := framework.WebhookSecret(t)
	event := map[string]string{"X-Forgejo-Event": "pull_request"}
	with := func(body []byte, hub, forgejo string) map[string]string {
		h := map[string]string{hubSignature: signature(hubSignature, hub, body), forgejoSignature: signature(forgejoSignature, forgejo, body)}
		for k, v := range event {
			h[k] = v
		}
		if forgejo == "" {
			delete(h, forgejoSignature)
		}
		return h
	}

	control := mergedEvent(t, "forgejo", "e2e/no-such-repo", 910000+pr.Number, "")
	since := time.Now()
	assert.Equal(t, http.StatusNoContent, e.PostSCMWebhook(t, with(control, secret, ""), control), "X-Hub-Signature-256 alone")
	e.WaitControllerLog(t, since, 30*time.Second, "the event signed in X-Hub-Signature-256 alone",
		framework.LogMessage("webhook received", "pr", strconv.Itoa(910000+pr.Number), "merged", "true"))
	assert.Equal(t, http.StatusNoContent, e.PostSCMWebhook(t, with(control, secret, "not-the-secret"), control),
		"a valid X-Hub-Signature-256 wins over a wrong X-Forgejo-Signature")

	body := mergedEvent(t, "forgejo", prs.Spec.Repo, pr.Number, "")
	assert.Equal(t, http.StatusUnauthorized, e.PostSCMWebhook(t, with(body, "not-the-secret", secret), body),
		"a wrong X-Hub-Signature-256 wins over a valid X-Forgejo-Signature")
	a.stillOpen(t, bundle, "prod", 10*time.Second, "after the refused event")

	ps, ok, err := e.Step(context.Background(), a.ns, pipelineName, bundle, "prod")
	require.NoError(t, err)
	require.True(t, ok)
	a.mergeSeenByWebhook(t, ps, pr, func(*v1alpha1.PRStatus) {
		require.Equal(t, http.StatusNoContent, e.PostSCMWebhook(t, with(body, secret, "not-the-secret"), body),
			"the merge event with a valid X-Hub-Signature-256 and a wrong X-Forgejo-Signature")
	})
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestForgejo_WebhookNonMergeEvents checks that signed events that are not a
// merge are accepted with 204 and change nothing: the PR opened, the PR
// closed without a merge, and a push event whose body says merged.
//
// Covers WEBHOOK-NONMERGE-01.
func TestForgejo_WebhookNonMergeEvents(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	a, bundle, pr, prs := prOpenWithoutWebhook(t, e)
	secret := framework.WebhookSecret(t)
	since := time.Now()
	for _, c := range []struct {
		what, event, action string
		merged              bool
		state               string
	}{
		{"opened", "pull_request", "opened", false, "open"},
		{"closed without a merge", "pull_request", "closed", false, "closed"},
		{"a push with a merged body", "push", "closed", true, "closed"},
	} {
		body := forgejoEvent(t, prs.Spec.Repo, pr.Number, c.action, c.merged, c.state)
		h := map[string]string{forgejoSignature: signature(forgejoSignature, secret, body), "X-Forgejo-Event": c.event}
		assert.Equal(t, http.StatusNoContent, e.PostSCMWebhook(t, h, body), c.what)
	}
	e.WaitControllerLog(t, since, 30*time.Second, "the push event read as a push",
		framework.LogMessage("webhook received", "event_type", "push", "merged", "false", "pr", strconv.Itoa(pr.Number)))
	a.stillOpen(t, bundle, "prod", 15*time.Second, "after the non-merge events")
	assert.Empty(t, e.ControllerLogLines(t, since, framework.LogMessage("PRStatus marked merged via webhook", "prstatus", prs.Name)))
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
}

// TestForgejo_ClosesPRs checks that a newer Bundle and waitForMergeTimeout
// each close the open Forgejo PR with a comment, fail the step, and leave
// nothing to merge.
//
// Covers SCM-FJ-06, STEP-MERGETIMEOUT-01.
func TestForgejo_ClosesPRs(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	scmClosesPRs(t, e, true)
}

// TestForgejo_Approvals checks that Forgejo reviews reach bundle.pr.
//
// Covers SCM-FJ-07.
func TestForgejo_Approvals(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	scmApprovals(t, e)
}

// TestForgejo_TokenCheck checks the startup token check on Forgejo: a token
// Forgejo rejects gets the scope warning and the controller still serves;
// the suite's token is accepted, and Forgejo cannot show its scopes.
//
// Covers SCM-FJ-09.
func TestForgejo_TokenCheck(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	ns := e.Namespace(t)
	scmRejectedToken(t, e, ns)
	scmTokenNotChecked(t, e, ns)
}

// TestForgejo_SCMAPIURL checks that --scm-api-url points the controller at
// the suite's Forgejo, and at another host when set to it.
//
// Covers SCM-FJ-10.
func TestForgejo_SCMAPIURL(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	scmAPIURL(t, e)
}

// TestForgejo_LabelsAndRotation checks PR labels and token rotation on
// Forgejo. The new token may push and open PRs (write:repository) but not
// label them (no write:issue). Not parallel: it changes the controller's
// token.
//
// Covers SCM-FJ-02, SCM-ROTATE-01.
func TestForgejo_LabelsAndRotation(t *testing.T) {
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	scmLabelsAndRotation(t, e, []string{"write:repository"})
}

// TestForgejo_PRBodySnapshot checks the PR body as a snapshot taken when
// the PR opens: the provenance row with commit, CI run link and author; the
// gate row with the override's author, reason and expiry; the upstream row.
// The gate is evaluated again every 10s and the body does not change. A
// Bundle without provenance gets dashes.
//
// Covers SCM-PRBODY-01.
func TestForgejo_PRBodySnapshot(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	a := newArgoApp(t, e, "test", "prod")
	e.CreateGate(t, framework.Gate(a.ns, "hold", "prod", openExpr, recheck))
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	first := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2,
		"--commit", "0123abc", "--author", "e2e-bot", "--ci-run-url", "https://ci.example/run/42")
	e.WaitStepState(t, a.ns, pipelineName, first, "test", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, first, "prod", "hold", false, "= false", gateTimeout)
	out := e.MustKardinal(t, a.ns, "override", pipelineName, "--stage", "prod", "--gate", "hold",
		"--reason", "INC-4521 hotfix", "--expires-in", "30m")
	m := createdByRE.FindStringSubmatch(out)
	require.Len(t, m, 2, "override output names its author:\n%s", out)
	_, pr := a.waitOpenPR(t, first, "prod")
	body := prBody(pr.Body)
	assert.Contains(t, body, "| "+fixtures.Image+" | "+fixtures.V2+" | — | [CI run](https://ci.example/run/42) | 0123abc | e2e-bot |",
		"provenance row")
	assert.Regexp(t, `\| hold \| `+regexp.QuoteMeta(a.ns)+` \| Pass \| OVERRIDDEN by `+regexp.QuoteMeta(m[1])+
		`: INC-4521 hotfix \(expires `+evaluatedAt+`\) \| `+evaluatedAt+` \|`, body, "gate row")
	assert.Regexp(t, upstreamRow, body, "upstream row")

	// Long enough for the minute in the gate's evaluation time to change.
	framework.Consistently(t, 65*time.Second, "PR #"+strconv.Itoa(pr.Number)+"'s body to stay as opened", func(context.Context) (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		prs, err := e.Git.PullRequests(ctx, a.repo)
		if err != nil {
			return false, err.Error()
		}
		for _, p := range prs {
			if p.Number == pr.Number {
				return prBody(p.Body) == body, "body:\n" + p.Body
			}
		}
		return false, "PR gone"
	})
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, first, "prod", "Verified", promoteTimeout)

	second := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV3)
	e.WaitStepState(t, a.ns, pipelineName, second, "test", "Verified", promoteTimeout)
	e.SetBundleLabel(t, a.ns, second, openLabel, "true")
	_, pr = a.waitOpenPR(t, second, "prod")
	body = prBody(pr.Body)
	assert.Contains(t, body, "| "+fixtures.Image+" | "+fixtures.V3+" | — | — | — | — |", "provenance row without provenance")
	assert.Regexp(t, `\| hold \| `+regexp.QuoteMeta(a.ns)+` \| Pass \| bundle\.version=`+regexp.QuoteMeta(fixtures.V3)+": "+
		regexp.QuoteMeta(openExpr)+` = true \| `+evaluatedAt+` \|`, body, "gate row")
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, second, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V3)
}

// TestForgejo_CircuitBreakerHonorsRateLimit points the controller at an API
// that answers 429 with Retry-After: 600 and checks the circuit opens until
// then and no request reaches the API while it is open. Not parallel: it
// changes the controller's --scm-api-url.
//
// Covers SCM-BREAKER-01.
func TestForgejo_CircuitBreakerHonorsRateLimit(t *testing.T) {
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	r := framework.NewReceiver(t)
	ns := e.Namespace(t)
	bucket := "breaker-" + ns[len(ns)-8:]
	r.FailRetryAfter(t, bucket, http.StatusTooManyRequests, 0, "600")
	since := time.Now()
	e.PatchController(t, func(spec *corev1.PodSpec) {
		framework.SetArg(spec, "scm-api-url", strings.TrimRight(r.URL(bucket, ""), "/"))
	})
	e.WaitControllerLog(t, since, time.Minute, "the controller to load its token", framework.LogMessage("SCM credentials loaded"))

	ctx := context.Background()
	for i := 1; i <= 6; i++ {
		p := &v1alpha1.PRStatus{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("breaker-%d", i), Namespace: ns},
			Spec: v1alpha1.PRStatusSpec{PRURL: fmt.Sprintf("http://example.invalid/e2e/breaker/pulls/%d", i),
				PRNumber: i, Repo: "e2e/breaker"},
		}
		require.NoError(t, e.Client.Create(ctx, p))
	}
	open := regexp.MustCompile(`SCM circuit open until (\S+)`)
	l := e.WaitControllerLog(t, since, 90*time.Second, "a poll refused by the open circuit", func(l framework.LogLine) bool {
		return framework.LogMessage("GetPRStatus failed, will retry", "namespace", ns)(l) && open.MatchString(l.Str("error"))
	})
	until, err := time.Parse(time.RFC3339, open.FindStringSubmatch(l.Str("error"))[1])
	require.NoError(t, err)
	assert.True(t, until.After(time.Now().Add(9*time.Minute)), "the circuit stays open for Retry-After's 600s: until %s", until)

	n := len(r.MustRecords(t, bucket))
	assert.NotZero(t, n, "the API was called before the circuit opened")
	framework.Consistently(t, 40*time.Second, "no request while the circuit is open", func(context.Context) (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		recs, err := r.Records(ctx, bucket)
		if err != nil {
			return false, err.Error()
		}
		return len(recs) == n, fmt.Sprintf("%d requests, %d when the circuit opened", len(recs), n)
	})
}

// TestForgejo_StaticTokenWithoutWebhookSecret runs the controller without
// KARDINAL_WEBHOOK_SECRET and without the token Secret settings. Without the
// secret every webhook is refused (Forgejo's own deliveries too) and merges
// come from polling. Without the Secret settings the token is static: a new
// token in the Secret is not used until a restart. Not parallel: it changes
// the controller.
//
// Covers WEBHOOK-NOSECRET-01, SCM-STATIC-01.
func TestForgejo_StaticTokenWithoutWebhookSecret(t *testing.T) {
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	// Before the change: the namespace copies the controller's token.
	a := newArgoApp(t, e, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	since := time.Now()
	restore := e.PatchController(t, func(spec *corev1.PodSpec) {
		for _, n := range []string{"KARDINAL_WEBHOOK_SECRET", "KARDINAL_SCM_TOKEN_SECRET_NAME",
			"KARDINAL_SCM_TOKEN_SECRET_NAMESPACE", "KARDINAL_SCM_TOKEN_SECRET_KEY"} {
			framework.SetEnv(spec, n, nil)
		}
	})
	e.WaitControllerLog(t, since, time.Minute, "webhooks disabled", framework.LogMessage("SCM webhooks disabled"))
	assert.False(t, e.SCMWebhookHealth(t).WebhookConfigured)
	probe := forgejoEvent(t, "e2e/no-such-repo", 1, "opened", false, "open")
	assert.Equal(t, http.StatusUnauthorized, e.PostSCMWebhook(t, signedPREvent(forgejoSignature, framework.WebhookSecret(t), probe), probe),
		"a signed event")
	assert.Equal(t, http.StatusUnauthorized, e.PostSCMWebhook(t, map[string]string{"X-Forgejo-Event": "pull_request"}, probe),
		"an unsigned event")

	restoreToken := e.SetSecretValue(t, framework.ControllerNamespace, framework.GitSecretName, "token", []byte(fakeToken))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps, pr := a.waitOpenPR(t, bundle, "prod")
	merged := time.Now()
	a.merge(t, pr)
	e.WaitControllerLog(t, merged, 90*time.Second, "the poll to find the merge",
		framework.LogMessage("PR merged — status updated", "prstatus", ps.Spec.PRStatusRef, "namespace", a.ns))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
	assert.Empty(t, e.ControllerLogLines(t, since, framework.LogMessage("PRStatus marked merged via webhook")), "no webhook is accepted")
	assert.Empty(t, e.ControllerLogLines(t, since, framework.LogMessage("SCM credential")), "no token watcher")

	since = time.Now()
	e.RestartController(t)
	e.WaitControllerLog(t, since, time.Minute, "the restarted controller to use the Secret's token",
		framework.LogMessage("SCM TOKEN SCOPE WARNING", "provider", "forgejo", "missing_scope", "<valid token>"))
	restoreToken()
	restore()
}

// TestForgejo_WebhookSecretNeedsRestart changes the webhook secret and
// checks the running controller keeps verifying with the old one until a
// restart, and with the new one after. Not parallel: it restarts the
// controller.
//
// Covers WEBHOOK-SECRET-01.
func TestForgejo_WebhookSecretNeedsRestart(t *testing.T) {
	e := framework.New(t)
	requireKind(t, e, "forgejo")
	old := framework.WebhookSecret(t)
	rotated := "e2e-rotated-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	probe := forgejoEvent(t, "e2e/no-such-repo", 1, "opened", false, "open")
	post := func(secret string) int {
		return e.PostSCMWebhook(t, signedPREvent(forgejoSignature, secret, probe), probe)
	}
	// Registered first, so it runs after the Secret is restored.
	done := false
	t.Cleanup(func() {
		if !done {
			e.RestartController(t)
		}
	})
	restore := e.SetSecretValue(t, framework.ControllerNamespace, "scm-webhook", "secret", []byte(rotated))
	framework.Consistently(t, 35*time.Second, "the running controller to keep the secret it started with", func(context.Context) (bool, string) {
		o, n := post(old), post(rotated)
		return o == http.StatusNoContent && n == http.StatusUnauthorized, fmt.Sprintf("old secret HTTP %d, new secret HTTP %d", o, n)
	})
	e.RestartController(t)
	assert.Equal(t, http.StatusNoContent, post(rotated), "the new secret after a restart")
	assert.Equal(t, http.StatusUnauthorized, post(old), "the old secret after a restart")
	restore()
	e.RestartController(t)
	done = true
	assert.Equal(t, http.StatusNoContent, post(old), "the old secret again")
}
