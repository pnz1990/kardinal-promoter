// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestStepStateChanged (#1509): only the PromotionStep updates the Pipeline
// status depends on requeue the Pipeline, the freeze holding a step included
// (#1974). Covers PERF-REQUEUE-01.
func TestStepStateChanged(t *testing.T) {
	step := func(state, msg, noChanges string, retry int) *kardinalv1alpha1.PromotionStep {
		s := &kardinalv1alpha1.PromotionStep{}
		s.Status.State, s.Status.Message, s.Status.RetryCount = state, msg, retry
		if noChanges != "" {
			s.Status.Outputs = map[string]string{"noChanges": noChanges}
		}
		return s
	}
	frozen := func(s *kardinalv1alpha1.PromotionStep, name string) *kardinalv1alpha1.PromotionStep {
		s.Status.FrozenBy = &kardinalv1alpha1.StepFreeze{Name: name}
		return s
	}
	cases := []struct {
		name     string
		old, new *kardinalv1alpha1.PromotionStep
		want     bool
	}{
		{"state changed", step("Promoting", "", "", 0), step("HealthChecking", "", "", 0), true},
		{"noChanges set", step("Promoting", "", "", 0), step("Promoting", "", "true", 0), true},
		{"a message", step("Promoting", "cloning", "", 0), step("Promoting", "pushing", "", 0), false},
		{"a retry", step("Promoting", "", "", 0), step("Promoting", "retrying", "", 1), false},
		{"a health check", step("HealthChecking", "a", "", 0), step("HealthChecking", "b", "", 0), false},
		// #1974: the Pipeline projects status.frozenBy per environment.
		{"a freeze holds it", step("Pending", "", "", 0), frozen(step("Pending", "", "", 0), "release-freeze"), true},
		{"the freeze is cleared", frozen(step("Pending", "", "", 0), "release-freeze"), step("Pending", "", "", 0), true},
		{"another freeze holds it", frozen(step("Pending", "", "", 0), "a"), frozen(step("Pending", "", "", 0), "b"), true},
		{"the same freeze, a new message", frozen(step("Pending", "x", "", 0), "a"), frozen(step("Pending", "y", "", 0), "a"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, stepStateChanged.Update(event.UpdateEvent{ObjectOld: tc.old, ObjectNew: tc.new}))
		})
	}
	assert.True(t, stepStateChanged.Create(event.CreateEvent{Object: step("", "", "", 0)}))
	assert.True(t, stepStateChanged.Delete(event.DeleteEvent{Object: step("Verified", "", "", 0)}))
}
