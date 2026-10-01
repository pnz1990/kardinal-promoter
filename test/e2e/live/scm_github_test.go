//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// The GitHub tests run in the github suite, whose controller runs with
// --scm-provider=github against github.com. Each test works on its own
// branch of one shared repo (hack/e2e/components/github.sh). github.com
// can't reach the kind cluster, so no test repo has a webhook: the webhook
// tests post the event GitHub would send, signed with the suite's secret.
//
// Not covered here: a label failure (SCM-GH-02) needs a token that may open
// PRs but not label them, and an approval (SCM-GH-07) a second account: a
// GitHub user can't approve their own PR.

// githubAPI is GitHub's public API, the default of --scm-api-url.
const githubAPI = "https://api.github.com"

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
// each close the open GitHub PR with a comment and fail the step.
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
// controller calls, as for a GitHub Enterprise server. Set to GitHub's API,
// the controller opens the PR through it. Set to a host that does not exist,
// the startup token check and the PRStatus poll call that host, and the step
// keeps waiting. Back on the suite's controller, the merge is seen and the
// step is Verified. The PR is opened before the host changes, so no branch
// is pushed without a PR that cleanup closes. Not parallel: it changes the
// controller's flags.
//
// Covers SCM-GH-10.
func TestGitHub_SCMAPIURL(t *testing.T) {
	e := framework.New(t)
	requireKind(t, e, "github")

	restore := e.PatchController(t, func(spec *corev1.PodSpec) { framework.SetArg(spec, "scm-api-url", githubAPI) })
	assert.Contains(t, controllerArgs(t, e), "--scm-api-url="+githubAPI)
	a := newArgoApp(t, e, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps, pr := a.waitOpenPR(t, bundle, "prod")
	e.WaitPRStatus(t, a.ns, pipelineName, bundle, "prod", time.Minute, "tracking the open PR",
		func(p *v1alpha1.PRStatus) bool { return p.Spec.PRNumber == pr.Number && p.Status.Open })

	since := time.Now()
	e.EditController(t, func(spec *corev1.PodSpec) { framework.SetArg(spec, "scm-api-url", "http://"+noSCMHost) })
	assert.Contains(t, controllerArgs(t, e), "--scm-api-url=http://"+noSCMHost)
	callsNoSCMHost := func(match func(framework.LogLine) bool) func(framework.LogLine) bool {
		return func(l framework.LogLine) bool { return match(l) && strings.Contains(l.Str("error"), noSCMHost) }
	}
	e.WaitControllerLog(t, since, time.Minute, "the token check to call "+noSCMHost,
		callsNoSCMHost(framework.LogMessage("SCM token scope check skipped", "provider", "github")))
	e.WaitControllerLog(t, since, 90*time.Second, "the PRStatus poll to call "+noSCMHost,
		callsNoSCMHost(framework.LogMessage("GetPRStatus failed, will retry", "prstatus", ps.Spec.PRStatusRef, "namespace", a.ns)))
	a.stillOpen(t, bundle, "prod", 10*time.Second, "while the API host does not resolve")

	restore()
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}
