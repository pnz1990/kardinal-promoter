//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// The Gitea tests run in the gitea suite, whose controller runs with
// --scm-provider=gitea against a Gitea server.

// TestGitea_PromotionPR checks the PR a pr-review environment opens on
// Gitea, and that rerunning open-pr finds it instead of opening another.
//
// Covers SCM-GT-01, SCM-GT-08.
func TestGitea_PromotionPR(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitea")
	assert.Contains(t, controllerArgs(t, e), "--scm-provider=gitea")
	scmPromotionPR(t, e, nil)
}

// TestGitea_MergeByPolling checks that without a webhook the PRStatus poll
// finds a merge on Gitea.
//
// Covers SCM-GT-03.
func TestGitea_MergeByPolling(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitea")
	scmMergeByPolling(t, e, nil)
}

// TestGitea_MergeWebhook checks a merge event signed in X-Gitea-Signature:
// it marks the PRStatus merged before a poll would, and the reconciler
// fetches the merge commit, which Gitea's event does not carry.
//
// Covers SCM-GT-04.
func TestGitea_MergeWebhook(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitea")
	a := newArgoAppIn(t, e, e.RepoWithoutWebhook, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	a.mergeByWebhook(t, bundle, "prod", giteaSignature, false)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGitea_WebhookBadSignature checks that a merge event with a wrong or
// no X-Gitea-Signature is refused and changes nothing.
//
// Covers SCM-GT-05.
func TestGitea_WebhookBadSignature(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitea")
	a, bundle, pr, _ := scmBadSignature(t, e, giteaSignature)
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
}

// TestGitea_ClosesPRs checks that a newer Bundle and waitForMergeTimeout
// each close the open Gitea PR with a comment and fail the step.
//
// Covers SCM-GT-06.
func TestGitea_ClosesPRs(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitea")
	scmClosesPRs(t, e, true)
}

// TestGitea_Approvals checks that Gitea reviews reach bundle.pr.
//
// Covers SCM-GT-07.
func TestGitea_Approvals(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitea")
	scmApprovals(t, e)
}

// TestGitea_TokenCheck checks the startup token check on Gitea: a token
// Gitea rejects gets the scope warning and the controller still serves; the
// suite's token is accepted, and Gitea cannot show its scopes.
//
// Covers SCM-GT-09.
func TestGitea_TokenCheck(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitea")
	ns := e.Namespace(t)
	scmRejectedToken(t, e, ns)
	scmTokenNotChecked(t, e, ns)
}

// TestGitea_SCMAPIURL checks that --scm-api-url points the controller at
// the suite's Gitea, and at another host when set to it.
//
// Covers SCM-GT-10.
func TestGitea_SCMAPIURL(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireKind(t, e, "gitea")
	scmAPIURL(t, e)
}

// TestGitea_LabelsAndRotation checks PR labels and token rotation on Gitea.
// The new token may push and open PRs (write:repository) but not label them
// (no write:issue). Not parallel: it changes the controller's token.
//
// Covers SCM-GT-02.
func TestGitea_LabelsAndRotation(t *testing.T) {
	e := framework.New(t)
	requireKind(t, e, "gitea")
	scmLabelsAndRotation(t, e, []string{"write:repository"})
}
