// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestChangeReachedGitAfter: the argoRollouts adapter compares a Healthy
// condition with the earliest time the change can have reached git (#1485
// review), not with the start of the health check: the PR's opening for a
// pr-review step, the start of git-push for a direct push, zero when nothing
// changed.
func TestChangeReachedGitAfter(t *testing.T) {
	at := func(m int) *metav1.Time {
		t := metav1.NewTime(time.Date(2026, 10, 9, 12, m, 0, 0, time.UTC))
		return &t
	}
	push := v1alpha1.StepStatus{Name: "git-push", StartedAt: at(1), CompletedAt: at(2)}
	openPR := v1alpha1.StepStatus{Name: openPRStep, StartedAt: at(3), CompletedAt: at(4)}
	health := v1alpha1.StepStatus{Name: "health-check", StartedAt: at(30)}
	tests := []struct {
		name   string
		status v1alpha1.PromotionStepStatus
		want   time.Time
	}{
		{name: "direct push: git-push start", status: v1alpha1.PromotionStepStatus{Steps: []v1alpha1.StepStatus{push, health}},
			want: at(1).Time},
		{name: "pr-review: the PR's opening", status: v1alpha1.PromotionStepStatus{Steps: []v1alpha1.StepStatus{push, openPR, health}},
			want: at(4).Time},
		{name: "no changes in git: zero", status: v1alpha1.PromotionStepStatus{Steps: []v1alpha1.StepStatus{push, health},
			Outputs: map[string]string{"noChanges": "true"}}},
		{name: "nothing recorded: zero", status: v1alpha1.PromotionStepStatus{Steps: []v1alpha1.StepStatus{health}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := changeReachedGitAfter(&v1alpha1.PromotionStep{Status: tt.status})
			assert.True(t, tt.want.Equal(got), "got %s, want %s", got, tt.want)
		})
	}
}

// TestBakeMaxDuration: bake.maxDuration replaces the default deadline
// (bake.minutes + health.timeout); a value shorter than one window counts as
// one window, and an unparsable one (admission rejects it) falls back to the
// default.
func TestBakeMaxDuration(t *testing.T) {
	env := func(minutes int, max string) v1alpha1.EnvironmentSpec {
		return v1alpha1.EnvironmentSpec{Bake: &v1alpha1.BakeConfig{Minutes: minutes, MaxDuration: max}}
	}
	tests := []struct {
		name string
		env  v1alpha1.EnvironmentSpec
		want time.Duration
	}{
		{name: "default", env: env(30, ""), want: 40 * time.Minute},
		{name: "a 24h bake with a 36h budget", env: env(24*60, "36h"), want: 36 * time.Hour},
		{name: "shorter than the window", env: env(30, "10m"), want: 30 * time.Minute},
		{name: "not a duration", env: env(30, "soon"), want: 40 * time.Minute},
		{name: "zero", env: env(30, "0"), want: 40 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, bakeMaxDuration(tt.env, 10*time.Minute))
		})
	}
}
