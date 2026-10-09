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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// prodStep is bundle's prod PromotionStep.
func prodStep(t *testing.T, a *app, bundle string) *v1alpha1.PromotionStep {
	t.Helper()
	ps, ok, err := a.e.Step(context.Background(), a.ns, pipelineName, bundle, "prod")
	require.NoError(t, err)
	require.True(t, ok, "%s has a prod step", bundle)
	return ps
}

// stepTiming is the status.steps entry name of ps.
func stepTiming(t *testing.T, ps *v1alpha1.PromotionStep, name string) v1alpha1.StepStatus {
	t.Helper()
	for _, s := range ps.Status.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("%s has no %s step in status.steps: %+v", ps.Name, name, ps.Status.Steps)
	return v1alpha1.StepStatus{}
}

// assertStepTimings checks the per-step timings of a Verified step: every
// entry of status.steps is Completed, starts no earlier than the previous
// one, and durationMs is completedAt - startedAt.
func assertStepTimings(t *testing.T, ps *v1alpha1.PromotionStep) {
	t.Helper()
	require.NotEmpty(t, ps.Status.Steps)
	assert.Equal(t, "health-check", ps.Status.Steps[len(ps.Status.Steps)-1].Name)
	var prev time.Time
	for _, s := range ps.Status.Steps {
		assert.Equal(t, v1alpha1.StepExecutionCompleted, s.State, s.Name)
		if !assert.NotNil(t, s.StartedAt, s.Name) || !assert.NotNil(t, s.CompletedAt, s.Name) {
			continue
		}
		assert.False(t, s.StartedAt.Time.Before(prev), "%s starts after the previous step", s.Name)
		assert.False(t, s.CompletedAt.Time.Before(s.StartedAt.Time), s.Name)
		assert.InDelta(t, s.CompletedAt.Sub(s.StartedAt.Time).Milliseconds(), s.DurationMs, 1000, "%s durationMs", s.Name)
		prev = s.StartedAt.Time
	}
}

// TestPipeline_StabilityMetrics checks the DORA stability pair in
// status.deploymentMetrics, the kardinal metrics rows and the UI API. A
// one-environment Pipeline (prod) gets: V2 Verified; a Bundle of a missing
// image whose health check times out (a failed deployment); V3 Verified,
// which restores it; then `kardinal rollback`, which rolls back from V3 (a
// second failure) and restores it when the rollback is Verified. The rate,
// and the mean time from each failed change reaching prod to the next
// Verified deployment, are what the step timestamps give.
// A Verified step records every step's timing in status.steps.
//
// Covers PIPE-DORA-02, STEP-TIMINGS-01.
func TestPipeline_StabilityMetrics(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	p := a.pipeline(nil)
	envSpec(t, p, "prod").Health.Timeout = "1m"
	a.apply(t, p)

	verified := func(bundle string) time.Time {
		t.Helper()
		e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
		e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
		c := findCond(prodStep(t, a, bundle).Status.Conditions, "Verified")
		require.Equal(t, metav1.ConditionTrue, c.Status)
		return c.LastTransitionTime.Time
	}
	metricsWith := func(deployments, failed int) *v1alpha1.PipelineDeploymentMetrics {
		t.Helper()
		return waitPipeline(t, e, a.ns, pipelineName, 2*time.Minute, fmt.Sprintf("%d deployments, %d failed", deployments, failed),
			func(p *v1alpha1.Pipeline) (bool, string) {
				m := p.Status.DeploymentMetrics
				return m != nil && m.Deployments == deployments && m.FailedDeployments == failed, fmt.Sprintf("%+v", m)
			}).Status.DeploymentMetrics
	}

	b1 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	verified(b1)
	assertStepTimings(t, prodStep(t, a, b1))
	m := metricsWith(1, 0)
	assert.Zero(t, m.ChangeFailureRateMillis)
	assert.Zero(t, m.RestoredFailures)

	broken := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
	ps := e.WaitStepState(t, a.ns, pipelineName, broken, "prod", "Failed", promoteTimeout)
	hc := stepTiming(t, ps, "health-check")
	require.NotNil(t, hc.StartedAt, "the broken image reached prod: its health check started")
	require.NotNil(t, hc.CompletedAt)
	assert.Equal(t, v1alpha1.StepExecutionFailed, hc.State)
	brokenDeployed := hc.StartedAt.Time
	m = metricsWith(2, 1)
	assert.Equal(t, 500, m.ChangeFailureRateMillis)
	assert.Zero(t, m.RestoredFailures, "not restored yet")

	b3 := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV3)
	restored1 := verified(b3)
	m = metricsWith(3, 1)
	assert.Equal(t, 333, m.ChangeFailureRateMillis)
	assert.Equal(t, 1, m.RestoredFailures)
	assert.Equal(t, int64(restored1.Sub(brokenDeployed).Minutes()), m.MeanTimeToRestoreMinutes,
		"from the broken change reaching prod to V3 Verified")
	b3Deployed := stepTiming(t, prodStep(t, a, b3), "health-check").StartedAt.Time

	out := e.MustKardinal(t, a.ns, "rollback", pipelineName, "--env", "prod")
	mm := regexp.MustCompile(`Bundle (\S+) created \(rollbackOf=(\S+)\)`).FindStringSubmatch(out)
	require.NotNil(t, mm, "rollback output:\n%s", out)
	rb := mm[1]
	assert.Equal(t, b1, mm[2], "the rollback restores V2")
	restored2 := verified(rb)
	a.running(t, "prod", imageV2, "the rollback is deployed")
	m = metricsWith(4, 2)
	assert.Equal(t, 500, m.ChangeFailureRateMillis, "the broken Bundle and V3, rolled back from")
	assert.Equal(t, 2, m.RestoredFailures)
	mean := (restored1.Sub(brokenDeployed) + restored2.Sub(b3Deployed)) / 2
	assert.Equal(t, int64(mean.Minutes()), m.MeanTimeToRestoreMinutes)

	rows := cells(e.MustKardinal(t, a.ns, "metrics", "--pipeline", pipelineName))
	byMetric := map[string][]string{}
	for _, r := range rows {
		byMetric[r[0]] = r
	}
	assert.Equal(t, []string{"change_failure_rate", "50.0%", "(2 of 4 deployments failed)"}, byMetric["change_failure_rate"])
	assert.Equal(t, []string{"time_to_restore", fmt.Sprintf("%dm", m.MeanTimeToRestoreMinutes), "(mean of 2 restored failures)"},
		byMetric["time_to_restore"])

	var pipes []struct {
		Name              string                              `json:"name"`
		Namespace         string                              `json:"namespace"`
		DeploymentMetrics *v1alpha1.PipelineDeploymentMetrics `json:"deploymentMetrics"`
	}
	mainUI(t, e).Get(t, uiAPI+"/pipelines").JSON(t, &pipes)
	found := false
	for _, p := range pipes {
		if p.Namespace == a.ns && p.Name == pipelineName {
			found = true
			require.NotNil(t, p.DeploymentMetrics, "the UI API carries deploymentMetrics")
			assert.Equal(t, 500, p.DeploymentMetrics.ChangeFailureRateMillis)
			assert.Equal(t, m.MeanTimeToRestoreMinutes, p.DeploymentMetrics.MeanTimeToRestoreMinutes)
			assert.Equal(t, 4, p.DeploymentMetrics.Deployments)
		}
	}
	assert.True(t, found, "the UI API lists %s/%s", a.ns, pipelineName)
}
