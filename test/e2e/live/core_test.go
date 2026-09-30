//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"slices"
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
// running Deployment, in that order.
func TestCore_PromoteThroughEnvironments(t *testing.T) {
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

// TestCore_ClosedPRFailsStep checks that closing a promotion PR without
// merging fails the step and leaves the environment on its old version.
func TestCore_ClosedPRFailsStep(t *testing.T) {
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	require.NoError(t, e.Git.ClosePR(context.Background(), a.repo, pr.Number))

	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", promoteTimeout)
	assert.True(t, strings.Contains(strings.ToLower(ps.Status.Message), "closed"),
		"the failure says the PR was closed: %q", ps.Status.Message)
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
}

// TestCore_NewerBundleSupersedes checks J6 supersession: a second Bundle
// created while the first waits on its prod PR supersedes it, and only the
// newer version reaches prod.
func TestCore_NewerBundleSupersedes(t *testing.T) {
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	older := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, older, "prod", "WaitingForMerge", promoteTimeout)
	newer := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)

	e.WaitBundlePhase(t, a.ns, older, "Superseded", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, newer, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, newer, "prod", "WaitingForMerge", promoteTimeout)

	pr := e.WaitPR(t, a.repo, time.Minute, "one open prod PR, for the newer Bundle", func(pr gitserver.PR) bool {
		return pr.State == "open"
	})
	assert.Contains(t, pr.Body, fixtures.V3)
	require.NoError(t, e.Git.MergePR(context.Background(), a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, newer, "prod", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V3, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))

	prs, err := e.Git.PullRequests(context.Background(), a.repo)
	require.NoError(t, err)
	states := make([]string, 0, len(prs))
	for _, p := range prs {
		states = append(states, p.State)
	}
	assert.False(t, slices.Contains(states, "open"), "the superseded Bundle's PR is closed: %v", states)
}
