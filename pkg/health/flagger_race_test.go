// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// TestFlaggerFailedBeforeTheTargetRanTheBundle proves the Flagger timing race
// fixed: the previous release's analysis can fail after this health check
// started but before the GitOps tool applied the Bundle to the target. That
// Failed is set after Since, so only the time a check first found the target
// on the Bundle images (TargetUpdatedAt) tells it from a Failed about the
// Bundle. Flagger notices a new target before it rolls back, so a Failed set
// after that time is about the Bundle.
func TestFlaggerFailedBeforeTheTargetRanTheBundle(t *testing.T) {
	bundle := []health.ImageExpectation{{Repository: podinfo, Tag: "6.15.0"}}
	target := map[string]interface{}{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web"}
	since := time.Date(2026, 9, 30, 22, 0, 0, 0, time.UTC)
	const (
		failedAt = "2026-09-30T22:01:30Z" // after since
		failMsg  = "Canary analysis failed, Deployment scaled to zero."
	)
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		require.NoError(t, err)
		return v
	}
	tests := []struct {
		name string
		// target is the image of the target Deployment.
		target   string
		expected []health.ImageExpectation
		since    time.Time
		seen     time.Time
		// ticked, when set, is a later Flagger tick's status.lastTransitionTime.
		ticked  string
		want    wantKind
		updated bool
		reason  string
	}{
		{name: "the previous release failed before the target ran the Bundle",
			target: podinfo + ":6.15.0", expected: bundle, since: since, seen: at("2026-09-30T22:02:00Z"),
			want: isProgressing, updated: true,
			reason: "Canary phase: Failed is for an earlier release: its Promoted condition's lastUpdateTime 2026-09-30T22:01:30Z " +
				"is not after the health check first found the target running the Bundle images (2026-09-30T22:02:00Z); " +
				"waiting for Flagger to analyze the new revision"},
		{name: "a Flagger tick that re-stamps status.lastTransitionTime does not date the Failed",
			target: podinfo + ":6.15.0", expected: bundle, since: since, seen: at("2026-09-30T22:02:00Z"),
			ticked: "2026-09-30T22:04:00Z", want: isProgressing, updated: true,
			reason: "lastUpdateTime 2026-09-30T22:01:30Z is not after"},
		{name: "the first check to find the target on the Bundle images does not trust a Failed",
			target: podinfo + ":6.15.0", expected: bundle, since: since,
			want: isProgressing, updated: true,
			reason: "Canary phase: Failed is for an earlier release: this check is the first to find the target running the Bundle images"},
		{name: "a Failed in the same second as the target update counts as earlier",
			target: podinfo + ":6.15.0", expected: bundle, since: since, seen: at("2026-09-30T22:01:30.600Z"),
			want: isProgressing, updated: true, reason: "is not after the health check first found the target"},
		{name: "a Failed after the target ran the Bundle images fails the Bundle",
			target: podinfo + ":6.15.0", expected: bundle, since: since, seen: at("2026-09-30T22:01:29Z"),
			want: isTerminal, updated: true, reason: "Canary phase: Failed — " + failMsg},
		{name: "a target not updated yet is Waiting and not recorded",
			target: podinfo + ":6.14.1", expected: bundle, since: since,
			want: isProgressing, reason: "target Deployment prod/web not updated yet"},
		{name: "without Bundle images the target update is unknown: the time check alone decides",
			target: podinfo + ":6.14.1", since: since,
			want: isTerminal, reason: failMsg},
		{name: "without a health-check start (no changes in git) a Failed on the Bundle images is terminal",
			target: podinfo + ":6.15.0", expected: bundle,
			want: isTerminal, updated: true, reason: failMsg},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := canaryObj(target, "Failed", failedAt, failMsg)
			if tt.ticked != "" {
				c.Object["status"].(map[string]interface{})["lastTransitionTime"] = tt.ticked
			}
			// Flagger scaled the failed canary's target to zero; the primary
			// still serves the release before the previous one.
			dyn := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), c,
				deploymentObj("web", 14, tt.target, 0), deploymentObj("web-primary", 4, podinfo+":6.14.0", 1))
			got, err := health.NewFlaggerAdapter(dyn).Check(context.Background(), health.CheckOptions{
				Flagger:         health.FlaggerConfig{Name: "web", Namespace: "prod"},
				ExpectedImages:  tt.expected,
				Since:           tt.since,
				TargetUpdatedAt: tt.seen,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
			assert.Equal(t, tt.updated, got.TargetUpdated, "TargetUpdated")
		})
	}
}
