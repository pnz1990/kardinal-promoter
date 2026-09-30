//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// TestCore_PromoteThroughEnvironments is the quickstart journey (J1): a
// Bundle auto-promotes through test and uat, waits on a PR for prod, and
// reaches prod when the PR merges. Each step must change git and then the
// running Deployment, in that order. Covers STEP-AUTO-01.
func TestCore_PromoteThroughEnvironments(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "uat", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	newImage := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", newImage,
		"--commit", "0123abc", "--author", "e2e-bot", "--ci-run-url", "https://ci.example/run/1")

	for _, env := range []string{"test", "uat"} {
		e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Verified", promoteTimeout)
		assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path(env)+"/kustomization.yaml"),
			"newTag: "+fixtures.V2, "%s: auto promotion commits to %s", env, a.repo.Branch)
		assert.Equal(t, newImage, e.DeploymentImage(t, a.ns, fixtures.Workload(env)),
			"%s: Verified means the new image is running", env)
	}

	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	assert.Subset(t, pr.Labels, []string{"kardinal", "kardinal/promotion"})
	for _, evidence := range []string{fixtures.V2, "0123abc", "e2e-bot", "https://ci.example/run/1"} {
		assert.Contains(t, pr.Body, evidence, "the PR body carries the Bundle's evidence")
	}
	assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("prod")+"/kustomization.yaml"),
		"newTag: "+fixtures.V1, "prod is unchanged until the PR merges")
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))

	framework.Consistently(t, 10*time.Second, "prod waits for the merge", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return ps.Status.State == "WaitingForMerge", ps.Status.State
	})

	ctx := context.Background()
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, newImage, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)

	prs, err := e.Git.PullRequests(ctx, a.repo)
	require.NoError(t, err)
	assert.Len(t, prs, 1, "one PR for one pr-review environment")
	assert.Equal(t, "merged", prs[0].State)
}

// closedGrace is prstatus.ClosedGracePeriod: how long a PR closed without
// merging can be reopened before the step fails (docs/troubleshooting.md).
const closedGrace = 5 * time.Minute

// TestCore_ClosedPRFailsStep checks the documented close path: a promotion PR
// closed without merging keeps the step waiting for closedGrace, then kardinal
// comments once that it stopped tracking the PR and fails the step. The
// environment stays on its old version. Covers SCM-CLOSED-01.
func TestCore_ClosedPRFailsStep(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	require.NoError(t, e.Git.ClosePR(context.Background(), a.repo, pr.Number))

	framework.Eventually(t, time.Minute, "the step to say the PR is closed", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return ps.Status.State == "WaitingForMerge" && strings.Contains(ps.Status.Message, "unless it is reopened"),
			fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})

	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", closedGrace+2*time.Minute)
	assert.Contains(t, ps.Status.Message, fmt.Sprintf("PR #%d was closed without merging and not reopened within 5m0s", pr.Number))
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("prod")+"/kustomization.yaml"), "newTag: "+fixtures.V1)

	framework.Eventually(t, time.Minute, "the stopped-tracking comment", func(context.Context) (bool, string) {
		n := len(e.PRComments(t, a.repo, pr.Number, "kardinal stopped tracking this PR"))
		return n > 0, fmt.Sprintf("%d comments", n)
	})
	framework.Consistently(t, 20*time.Second, "kardinal comments once", func(context.Context) (bool, string) {
		n := len(e.PRComments(t, a.repo, pr.Number, "kardinal stopped tracking this PR"))
		return n == 1, fmt.Sprintf("%d stopped-tracking comments", n)
	})
}

// TestCore_ReopenedPRContinues checks the other half of the close path: a PR
// reopened within closedGrace keeps the step waiting, and merging it then
// promotes as usual. Covers SCM-REOPEN-01.
func TestCore_ReopenedPRContinues(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	newImage := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", newImage)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	ctx := context.Background()
	require.NoError(t, e.Git.ClosePR(ctx, a.repo, pr.Number))
	framework.Eventually(t, time.Minute, "the step to see the close", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return strings.Contains(ps.Status.Message, "is closed"), fmt.Sprintf("message=%q", ps.Status.Message)
	})

	require.NoError(t, e.Git.ReopenPR(ctx, a.repo, pr.Number))
	framework.Eventually(t, time.Minute, "the step to drop the closed message", func(ctx context.Context) (bool, string) {
		ps, _, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil || ps == nil {
			return false, "step lookup failed"
		}
		return ps.Status.State == "WaitingForMerge" && !strings.Contains(ps.Status.Message, "is closed"),
			fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, newImage, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	assert.Empty(t, e.PRComments(t, a.repo, pr.Number, "kardinal stopped tracking this PR"))
}

// TestCore_NewerBundleSupersedes checks J6 supersession: a second Bundle
// created while the first waits on its prod PR supersedes it. kardinal closes
// the older PR with a comment, and only the newer version reaches prod.
// Covers BUNDLE-SUPERSEDE-02.
func TestCore_NewerBundleSupersedes(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	older := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, older, "prod", "WaitingForMerge", promoteTimeout)
	olderPR := e.WaitPR(t, a.repo, time.Minute, "the older Bundle's prod PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	newer := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)

	e.WaitBundlePhase(t, a.ns, older, "Superseded", promoteTimeout)
	// The PromotionStep reconciler watches Bundles, so the older step fails and
	// its PR closes at once, not on the next 30s WaitingForMerge poll.
	ps := e.WaitStepState(t, a.ns, pipelineName, older, "prod", "Failed", 15*time.Second)
	assert.Contains(t, ps.Status.Message, "superseded")
	e.WaitPRState(t, a.repo, olderPR.Number, "closed", 15*time.Second)
	assert.Len(t, e.PRComments(t, a.repo, olderPR.Number, "kardinal closed this PR: bundle "+older+" was superseded"), 1)

	e.WaitStepState(t, a.ns, pipelineName, newer, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, newer, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "one open prod PR, for the newer Bundle", func(pr gitserver.PR) bool {
		return pr.State == "open"
	})
	assert.Contains(t, pr.Body, fixtures.V3)
	require.NoError(t, e.Git.MergePR(context.Background(), a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, newer, "prod", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V3, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	e.WaitBundlePhase(t, a.ns, newer, "Verified", time.Minute)
	assert.Equal(t, "closed", e.WaitPRState(t, a.repo, olderPR.Number, "closed", time.Second).State,
		"the superseded PR was never merged")
}
