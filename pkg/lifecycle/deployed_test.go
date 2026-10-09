// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestDeployedBundle: the deployed Bundle of an environment is the one whose
// change landed there last. A newer Bundle still waiting for its merge, or
// failed before its health check, has not replaced it. Steps of other
// pipelines and environments do not count.
func TestDeployedBundle(t *testing.T) {
	failedAfterHealth := func(bundle string, minute int) *v1alpha1.PromotionStep {
		s := step(bundle, "app", "prod", "Failed", minute)
		exp := metav1.NewTime(s.CreationTimestamp.Add(10 * time.Minute))
		s.Status.HealthCheckExpiry = &exp
		return s
	}
	tests := []struct {
		name  string
		steps []*v1alpha1.PromotionStep
		want  string
	}{
		{name: "nothing landed", want: ""},
		{name: "only a step waiting for merge",
			steps: []*v1alpha1.PromotionStep{step("v1", "app", "prod", "WaitingForMerge", 1)}, want: ""},
		{name: "verified",
			steps: []*v1alpha1.PromotionStep{step("v1", "app", "prod", "Verified", 1)}, want: "v1"},
		{name: "a newer Bundle waiting for merge does not replace it",
			steps: []*v1alpha1.PromotionStep{
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "WaitingForMerge", 5),
			}, want: "v1"},
		{name: "a newer Bundle failed before its health check does not replace it",
			steps: []*v1alpha1.PromotionStep{
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "Failed", 5),
			}, want: "v1"},
		{name: "a newer Bundle health checking replaces it",
			steps: []*v1alpha1.PromotionStep{
				step("v1", "app", "prod", "Verified", 1), step("v2", "app", "prod", "HealthChecking", 5),
			}, want: "v2"},
		{name: "a newer Bundle failed during its health check replaces it",
			steps: []*v1alpha1.PromotionStep{step("v1", "app", "prod", "Verified", 1), failedAfterHealth("v2", 5)},
			want:  "v2"},
		{name: "other environments and pipelines are ignored",
			steps: []*v1alpha1.PromotionStep{
				step("v1", "app", "prod", "Verified", 1),
				step("v2", "app", "test", "Verified", 5),
				step("o1", "other", "prod", "Verified", 9),
			}, want: "v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := make([]v1alpha1.PromotionStep, 0, len(tt.steps))
			for _, s := range tt.steps {
				steps = append(steps, *s)
			}
			assert.Equal(t, tt.want, lifecycle.DeployedBundle(steps, "app", "prod"))
		})
	}
}

// TestDeployedBundleMatching: only the Bundles match accepts count, and the
// newest of them that landed wins (#1353).
func TestDeployedBundleMatching(t *testing.T) {
	steps := []v1alpha1.PromotionStep{
		*step("img1", "app", "prod", "Verified", 1),
		*step("cfg1", "app", "prod", "Verified", 2),
		*step("img2", "app", "prod", "Verified", 3),
		*step("cfg2", "app", "prod", "WaitingForMerge", 4),
		*step("cfg9", "app", "test", "Verified", 9),
	}
	isConfig := func(b string) bool { return len(b) > 3 && b[:3] == "cfg" }
	isImage := func(b string) bool { return len(b) > 3 && b[:3] == "img" }
	assert.Equal(t, "img2", lifecycle.DeployedBundle(steps, "app", "prod"))
	assert.Equal(t, "cfg1", lifecycle.DeployedBundleMatching(steps, "app", "prod", isConfig),
		"cfg2 has not landed, cfg9 is another environment")
	assert.Equal(t, "img2", lifecycle.DeployedBundleMatching(steps, "app", "prod", isImage))
	assert.Empty(t, lifecycle.DeployedBundleMatching(steps, "app", "prod", func(string) bool { return false }))
}
