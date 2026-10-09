//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestSCM_MetricsAfterPromotion promotes a pr-review environment and
// reads the controller's /metrics: the PR the step opened counts as a
// successful SCM call of the cluster's provider, the clone and the push
// count with their durations, the bytes of both are counted (the e2e git
// server speaks smart HTTP), every circuit gauge is closed or a known state,
// and the owner and operation labels stay within their caps.
//
// Covers OBS-SCM-01.
func TestSCM_MetricsAfterPromotion(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	before, err := e.ControllerMetrics(ctx)
	require.NoError(t, err)
	// The metrics' provider label: Gitea is served by the Forgejo provider.
	provider := os.Getenv(framework.EnvSCMProvider)
	if provider == "gitea" {
		provider = "forgejo"
	}
	require.NotEmpty(t, provider)
	openPR := "POST pulls" // GitHub, Forgejo, Gitea
	if provider == "gitlab" {
		openPR = "POST merge_requests"
	}
	opened := func(m framework.Metrics) float64 {
		return m.Sum("kardinal_scm_requests_total", map[string]string{"provider": provider, "operation": openPR, "result": "ok"})
	}

	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "WaitingForMerge", promoteTimeout)

	var after framework.Metrics
	framework.Eventually(t, time.Minute, "SCM and git metrics after the promotion", func(ctx context.Context) (bool, string) {
		after, err = e.ControllerMetrics(ctx)
		if err != nil {
			return false, err.Error()
		}
		return opened(after) > opened(before), fmt.Sprintf("%s ok %v -> %v", openPR, opened(before), opened(after))
	})
	for _, op := range []string{"clone", "push"} {
		assert.Greater(t, after.Sum("kardinal_git_operations_total", map[string]string{"operation": op, "result": "ok"}),
			before.Sum("kardinal_git_operations_total", map[string]string{"operation": op, "result": "ok"}), op)
		assert.Positive(t, after.Sum("kardinal_git_operation_duration_seconds_count", map[string]string{"operation": op}), op)
	}
	assert.Greater(t, after.Sum("kardinal_git_transfer_bytes_total", map[string]string{"service": "fetch", "direction": "received"}),
		before.Sum("kardinal_git_transfer_bytes_total", map[string]string{"service": "fetch", "direction": "received"}), "clone bytes")
	assert.Greater(t, after.Sum("kardinal_git_transfer_bytes_total", map[string]string{"service": "push", "direction": "sent"}),
		before.Sum("kardinal_git_transfer_bytes_total", map[string]string{"service": "push", "direction": "sent"}), "push bytes")
	assert.Positive(t, after.Sum("kardinal_scm_request_duration_seconds_count", map[string]string{"provider": provider}))
	assert.True(t, after.Has("kardinal_scm_circuit_state", map[string]string{"provider": provider, "owner": "quota"}))
	for _, s := range after {
		if s.Name == "kardinal_scm_circuit_state" {
			assert.Contains(t, []float64{0, 1, 2}, s.Value, "circuit state %v", s.Labels)
		}
	}
	assert.LessOrEqual(t, len(after.LabelValues("kardinal_scm_requests_total", "owner")), 51, "owner label capped (50 + other)")
	assert.LessOrEqual(t, len(after.LabelValues("kardinal_scm_requests_total", "operation")), 101, "operation label capped")
	for _, owner := range after.LabelValues("kardinal_scm_requests_total", "owner") {
		assert.NotContains(t, owner, "/", "owners, never repositories")
	}
}
