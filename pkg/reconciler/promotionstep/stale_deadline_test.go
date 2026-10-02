// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestResourceRecordsWhenTheTemplateRanTheBundle proves the reconciler half of
// the B92 fix: the first resource health check that finds the pod template on
// the Bundle images records the time in status.targetUpdatedAt, once, and the
// checks after it pass that time to the resource check. A
// ProgressDeadlineExceeded the Deployment controller kept from an earlier
// rollout (a rollback to the ReplicaSet before a stalled one) does not fail
// the step; one set after the template update does.
func TestResourceRecordsWhenTheTemplateRanTheBundle(t *testing.T) {
	env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource"}}
	v2 := []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2"}}
	started := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
	steps := []v1alpha1.StepStatus{{Name: "health-check", State: v1alpha1.StepExecutionInProgress, StartedAt: &started}}
	after := func(d time.Duration) *metav1.Time { t := metav1.NewTime(started.Add(d)); return &t }
	// stalled is the Deployment on image whose Progressing condition the
	// controller set to ProgressDeadlineExceeded at deadline.
	stalled := func(image string, deadline time.Duration) *appsv1.Deployment {
		d := withImage(stalledDeployment("p", "test"), image)
		c := &d.Status.Conditions[len(d.Status.Conditions)-1]
		c.LastUpdateTime, c.LastTransitionTime = *after(deadline), *after(deadline)
		c.Message = `ReplicaSet "p-5d8f" has timed out progressing.`
		return d
	}
	tests := []struct {
		name         string
		deploy       *appsv1.Deployment
		seen         *metav1.Time
		wantState    string
		wantMsg      string
		wantFailures int
		// wantSeen is the expected status.targetUpdatedAt; recorded means
		// set by this reconcile (about now).
		wantSeen *metav1.Time
		recorded bool
	}{
		{name: "a deadline the rolled back Deployment kept from the stalled rollout does not fail the step",
			deploy:    stalled("ghcr.io/org/app:v2", -30*time.Second),
			wantState: "HealthChecking", wantMsg: "is from an earlier rollout: its lastUpdateTime",
			recorded: true},
		{name: "the first check to find the template on the Bundle records it and does not trust the deadline",
			deploy:    stalled("ghcr.io/org/app:v2", 20*time.Second),
			wantState: "HealthChecking", wantMsg: "this check is the first to find the pod template running the Bundle images",
			recorded: true},
		{name: "a deadline set before the template ran the Bundle is an earlier rollout's",
			deploy: stalled("ghcr.io/org/app:v2", 20*time.Second), seen: after(30 * time.Second),
			wantState: "HealthChecking", wantMsg: "is not after the health check first found the pod template",
			wantSeen: after(30 * time.Second)},
		{name: "a deadline set after the template ran the Bundle fails the step",
			deploy: stalled("ghcr.io/org/app:v2", 20*time.Second), seen: after(10 * time.Second),
			wantState: "Failed", wantMsg: "rollout failed: ProgressDeadlineExceeded", wantFailures: 1,
			wantSeen: after(10 * time.Second)},
		{name: "a template not updated yet records nothing",
			deploy:    stalled("ghcr.io/org/app:v1", 20*time.Second),
			wantState: "HealthChecking", wantMsg: "not updated yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now().Truncate(time.Second)
			_, got, _ := healthCase{env: env, images: v2,
				status: v1alpha1.PromotionStepStatus{Steps: steps, TargetUpdatedAt: tt.seen},
				objs:   []client.Object{tt.deploy},
			}.run(t)
			assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
			assert.Contains(t, got.Status.Message, tt.wantMsg)
			assert.Equal(t, tt.wantFailures, got.Status.ConsecutiveHealthFailures)
			switch {
			case tt.recorded:
				require.NotNil(t, got.Status.TargetUpdatedAt)
				assert.False(t, got.Status.TargetUpdatedAt.Time.Before(before), "recorded at this check")
			case tt.wantSeen != nil:
				require.NotNil(t, got.Status.TargetUpdatedAt)
				assert.True(t, tt.wantSeen.Time.Equal(got.Status.TargetUpdatedAt.Time), "set once: %s", got.Status.TargetUpdatedAt)
			default:
				assert.Nil(t, got.Status.TargetUpdatedAt)
			}
		})
	}
}
