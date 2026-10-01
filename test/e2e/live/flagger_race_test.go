//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestFlagger_PreviousReleaseFailsBeforeSync checks the Flagger timing race: a
// broken release's analysis fails after the next release's health check
// started but before Argo CD applied the next release. Once Argo CD applies
// it, the Canary is Failed, set after that health check started, and the
// primary is on an older release. Until Flagger's next analysis tick notices
// the new target, that Failed is the broken release's and must not fail the
// next one. The test suspends the Canary so that Flagger's next tick waits
// for two health checks instead of up to one analysis interval. The next
// release is Verified once Flagger promotes it.
//
// Covers HEALTH-FLAG-04.
func TestFlagger_PreviousReleaseFailsBeforeSync(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newCanaryApp(t, e, "prod")
	a.apply(t, a.deliveryPipeline(flagger, false, nil))
	canary, v2 := fixtures.Workload("prod"), fixtures.Image+":"+fixtures.V2
	const earlier = "waiting for flagger: Canary phase: Failed is for an earlier release"

	// Flagger analyzes the broken release and fails it at the progress deadline.
	broken := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
	e.WaitStepMessageAll(t, a.ns, pipelineName, broken, "prod", "HealthChecking", deliveryTimeout,
		"waiting for flagger: Canary phase: Progressing")

	// V2's health check starts while Argo CD holds the cluster on the broken
	// release, and the broken release fails after that.
	e.SetArgoAutoSync(t, a.argoApp("prod"), false)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", v2)
	ps := e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, "prod", "HealthChecking", promoteTimeout,
		notUpdated(a.ns, flagger, "prod"))
	started := healthCheckStarted(t, ps)
	e.WaitCanaryPhase(t, a.ns, canary, "Failed", deliveryTimeout)
	failedAt := e.CanaryPhaseSetAt(t, a.ns, canary)
	require.False(t, failedAt.Before(started),
		"the broken release failed (%s) after V2's health check started (%s)", failedAt, started)

	// Argo CD applies V2 while Flagger's next tick waits.
	e.SuspendCanary(t, a.ns, canary, true)
	e.SetArgoAutoSync(t, a.argoApp("prod"), true)
	e.WaitDeploymentImage(t, a.ns, canary, v2, syncTimeout)
	e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, "prod", "HealthChecking", deliveryTimeout, earlier)
	e.HoldStep(t, deliveryHold, a.ns, pipelineName, bundle, "prod", "the broken release's Failed does not fail V2",
		func(ps *v1alpha1.PromotionStep) bool {
			return ps.Status.State == "HealthChecking" && ps.Status.ConsecutiveHealthFailures == 0 &&
				strings.Contains(ps.Status.Message, earlier)
		})

	// Flagger notices V2, analyzes and promotes it.
	e.SuspendCanary(t, a.ns, canary, false)
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", deliveryTimeout)
	assert.Contains(t, ps.Status.Message, "health check passed via flagger: Canary phase: Succeeded")
	if assert.NotNil(t, ps.Status.TargetUpdatedAt, "status.targetUpdatedAt") {
		assert.True(t, ps.Status.TargetUpdatedAt.After(failedAt),
			"kardinal saw the target on V2 (%s) after the broken release failed (%s)", ps.Status.TargetUpdatedAt, failedAt)
	}
	assert.Equal(t, v2, e.DeploymentImage(t, a.ns, fixtures.PrimaryWorkload("prod")))
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// healthCheckStarted is when the step's health-check step started.
func healthCheckStarted(t *testing.T, ps *v1alpha1.PromotionStep) time.Time {
	t.Helper()
	for _, s := range ps.Status.Steps {
		if s.Name == "health-check" && s.StartedAt != nil {
			return s.StartedAt.Time
		}
	}
	t.Fatalf("step %s has no started health-check step: %+v", ps.Name, ps.Status.Steps)
	return time.Time{}
}
