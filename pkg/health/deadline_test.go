// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// TestDeploymentDeadlineFromAnEarlierRollout proves B92 fixed: a rollout back
// to an existing ReplicaSet (a rollback to the revision before a stalled one)
// creates no ReplicaSet, so the Deployment controller keeps the stalled
// rollout's ProgressDeadlineExceeded, with its time and message, after it
// observed the new template, until it sees the stalled pods go. Like the
// flagger adapter's Failed phase, the condition counts only when set after the
// health check started and after a check first found the pod template on the
// Bundle images (TargetUpdatedAt).
func TestDeploymentDeadlineFromAnEarlierRollout(t *testing.T) {
	v2 := []health.ImageExpectation{{Repository: "ghcr.io/org/app", Tag: "v2"}}
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		require.NoError(t, err)
		return v
	}
	const (
		deadlineAt = "2026-10-02T05:01:30Z"
		stalled    = `ReplicaSet "web-5d8f" has timed out progressing.`
	)
	since := at("2026-10-02T05:00:00Z")
	tests := []struct {
		name     string
		image    string
		expected []health.ImageExpectation
		since    time.Time
		seen     time.Time
		// transitionOnly sets only the condition's lastTransitionTime.
		transitionOnly bool
		want           wantKind
		updated        bool
		reason         string
	}{
		{name: "a deadline before this health check started is from an earlier rollout",
			image: "ghcr.io/org/app:v2", expected: v2, since: at("2026-10-02T05:02:00Z"),
			want: isProgressing, updated: true,
			reason: `Deployment prod/web: ProgressDeadlineExceeded (` + stalled + `) is from an earlier rollout: ` +
				"its lastUpdateTime 2026-10-02T05:01:30Z is before this health check started (2026-10-02T05:02:00Z); " +
				"waiting for the Deployment controller to see this rollout progress"},
		{name: "lastTransitionTime dates the deadline when lastUpdateTime is not set",
			image: "ghcr.io/org/app:v2", expected: v2, since: at("2026-10-02T05:02:00Z"), transitionOnly: true,
			want: isProgressing, updated: true,
			reason: "its lastTransitionTime 2026-10-02T05:01:30Z is before this health check started"},
		{name: "the first check to find the pod template on the Bundle images does not trust a deadline",
			image: "ghcr.io/org/app:v2", expected: v2, since: since,
			want: isProgressing, updated: true,
			reason: "is from an earlier rollout: this check is the first to find the pod template running the Bundle images"},
		{name: "a deadline not after the template update is from an earlier rollout",
			image: "ghcr.io/org/app:v2", expected: v2, since: since, seen: at("2026-10-02T05:02:00Z"),
			want: isProgressing, updated: true,
			reason: "its lastUpdateTime 2026-10-02T05:01:30Z is not after the health check first found the pod template " +
				"running the Bundle images (2026-10-02T05:02:00Z)"},
		{name: "a deadline in the same second as the template update counts as earlier",
			image: "ghcr.io/org/app:v2", expected: v2, since: since, seen: at("2026-10-02T05:01:30.600Z"),
			want: isProgressing, updated: true, reason: "is not after the health check first found the pod template"},
		{name: "a deadline after the template update fails the Bundle",
			image: "ghcr.io/org/app:v2", expected: v2, since: since, seen: at("2026-10-02T05:01:29Z"),
			want: isTerminal, updated: true,
			reason: "Deployment prod/web rollout failed: ProgressDeadlineExceeded: " + stalled},
		{name: "a pod template not on the Bundle images is Waiting and not recorded",
			image: "ghcr.io/org/app:v1", expected: v2, since: since,
			want: isProgressing, reason: "not updated yet"},
		{name: "without Bundle images the template update is unknown: the time check alone decides",
			image: "ghcr.io/org/app:v2", since: since,
			want: isTerminal, reason: "rollout failed: ProgressDeadlineExceeded"},
		{name: "a workload on none of the Bundle repositories is not updated: the time check alone decides",
			image: "registry.local:5000/other:v9", expected: v2, since: since, seen: at("2026-10-02T05:02:00Z"),
			want: isTerminal, reason: "rollout failed: ProgressDeadlineExceeded"},
		{name: "the time check alone still waits on a deadline before the health check started",
			image: "registry.local:5000/other:v9", expected: v2, since: at("2026-10-02T05:02:00Z"),
			want: isProgressing, reason: "is before this health check started"},
		{name: "a deadline in the same second as the health check start counts",
			image: "registry.local:5000/other:v9", expected: v2, since: at("2026-10-02T05:01:30.400Z"),
			want: isTerminal, reason: "rollout failed: ProgressDeadlineExceeded"},
		{name: "without a health-check start (no changes in git) a deadline on the Bundle images is terminal",
			image: "ghcr.io/org/app:v2", expected: v2,
			want: isTerminal, updated: true, reason: "rollout failed: ProgressDeadlineExceeded: " + stalled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The stalled ReplicaSet's pod has not gone yet: the template's
			// ReplicaSet runs both replicas, and one stalled replica is left.
			d := deployment("web", tt.image, func(d *appsv1.Deployment) {
				d.Status.Replicas, d.Status.UnavailableReplicas = 3, 1
				setProgressing(d, corev1.ConditionFalse, "ProgressDeadlineExceeded")
				d.Status.Conditions[1].Message = stalled
				d.Status.Conditions[1].LastTransitionTime = metav1.NewTime(at(deadlineAt))
				if !tt.transitionOnly {
					d.Status.Conditions[1].LastUpdateTime = metav1.NewTime(at(deadlineAt))
				}
			})
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(d).Build()
			got, err := health.NewDeploymentAdapter(c).Check(context.Background(), health.CheckOptions{
				Resource:        health.ResourceConfig{Name: "web", Namespace: "prod"},
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

// TestDeploymentTargetUpdated proves the resource adapter reports the target
// updated (HealthStatus.TargetUpdated) only when it compared the pod template
// with the Bundle images and found them: with health.labelSelector, every
// matching Deployment that runs a Bundle repository must run the Bundle's
// image, and one must.
func TestDeploymentTargetUpdated(t *testing.T) {
	v2 := []health.ImageExpectation{{Repository: "ghcr.io/org/app", Tag: "v2"}}
	other := "registry.local:5000/other:v9"
	tests := []struct {
		name     string
		images   []string
		expected []health.ImageExpectation
		want     wantKind
		updated  bool
	}{
		{name: "a rolled out Deployment on the Bundle image",
			images: []string{"ghcr.io/org/app:v2"}, expected: v2, want: isHealthy, updated: true},
		{name: "a Deployment still on the previous image",
			images: []string{"ghcr.io/org/app:v1"}, expected: v2, want: isProgressing},
		{name: "a Deployment on none of the Bundle repositories",
			images: []string{other}, expected: v2, want: isHealthy},
		{name: "no Bundle images to compare",
			images: []string{"ghcr.io/org/app:v2"}, want: isHealthy},
		{name: "every matching Deployment on the Bundle image",
			images: []string{"ghcr.io/org/app:v2", "ghcr.io/org/app:v2"}, expected: v2, want: isHealthy, updated: true},
		{name: "one matching Deployment still on the previous image",
			images: []string{"ghcr.io/org/app:v2", "ghcr.io/org/app:v1"}, expected: v2, want: isProgressing},
		{name: "a matching Deployment on another repository does not count",
			images: []string{"ghcr.io/org/app:v2", other}, expected: v2, want: isHealthy, updated: true},
		{name: "no matching Deployment on a Bundle repository",
			images: []string{other, other}, expected: v2, want: isHealthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(buildScheme(t))
			opts := health.CheckOptions{ExpectedImages: tt.expected,
				Resource: health.ResourceConfig{Name: "web-0", Namespace: "prod"}}
			if len(tt.images) > 1 {
				opts.Resource.LabelSelector = map[string]string{"app": "web"}
			}
			for i, img := range tt.images {
				b = b.WithObjects(deployment("web-"+string(rune('0'+i)), img, nil))
			}
			got, err := health.NewDeploymentAdapter(b.Build()).Check(context.Background(), opts)
			require.NoError(t, err)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Equal(t, tt.updated, got.TargetUpdated, "TargetUpdated")
		})
	}
}
