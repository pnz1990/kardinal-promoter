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
	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
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
