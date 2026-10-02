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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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

// TestFluxStalledOnAnEarlierDeadline proves the Flux twin of B92 fixed: Flux
// (kstatus) fails a Deployment on any ProgressDeadlineExceeded, also one the
// Deployment controller kept from an earlier rollout after a rollback to the
// ReplicaSet before a stalled one, and does not check again until its next
// reconcile. That "failed early due to stalled resources" on the promoted
// commit waits for Flux when every resource it lists is a Deployment whose
// condition is from before the health check started, or that is no longer
// past its progress deadline. Any other stall stays terminal.
func TestFluxStalledOnAnEarlierDeadline(t *testing.T) {
	bundle := []health.ImageExpectation{{Repository: podinfo, Tag: "6.15.0"}}
	since, _ := time.Parse(time.RFC3339, "2026-10-02T05:02:00Z")
	const (
		before = "2026-10-02T05:01:30Z"
		after  = "2026-10-02T05:12:30Z"
		web    = "[Deployment/prod/web status: 'Failed']"
	)
	// stalledAt is Deployment prod/<name> on the Bundle image, past a
	// progress deadline the controller set at at.
	stalledAt := func(name, at string) *unstructured.Unstructured {
		d := stalledDeployment(name, podinfo+":6.15.0")
		c := d.Object["status"].(map[string]interface{})["conditions"].([]interface{})[1].(map[string]interface{})
		c["lastUpdateTime"], c["lastTransitionTime"] = at, at
		c["message"] = `ReplicaSet "` + name + `-5d8f" has timed out progressing.`
		return d
	}
	stalledKs := func(listed, attempted string, mutate func(obj map[string]interface{})) *unstructured.Unstructured {
		return kustomization("False", "HealthCheckFailed",
			"health check failed after 5.01s: failed early due to stalled resources: "+listed,
			"main@sha1:"+fluxPrevious, both(withInventory("web"), func(obj map[string]interface{}) {
				obj["status"].(map[string]interface{})["lastAttemptedRevision"] = "main@sha1:" + attempted
			}, func(obj map[string]interface{}) {
				if mutate != nil {
					mutate(obj)
				}
			}))
	}
	const earlier = "(lastAttemptedRevision=034ce92a1b2c), but no Deployment Flux lists is past a progress deadline " +
		"set during this promotion: Deployment prod/web: ProgressDeadlineExceeded (ReplicaSet \"web-5d8f\" has timed out " +
		"progressing.) is from an earlier rollout: its lastUpdateTime 2026-10-02T05:01:30Z is before this health check " +
		"started (2026-10-02T05:02:00Z); waiting for the Deployment controller to see this rollout progress; " +
		"waiting for Flux to check again"
	tests := []struct {
		name   string
		objs   []runtime.Object
		since  time.Time
		want   wantKind
		reason string
	}{
		{name: "a deadline from before the health check started waits for Flux",
			objs: []runtime.Object{stalledKs(web, fluxPushed, nil), stalledAt("web", before)}, since: since,
			want: isProgressing, reason: earlier},
		{name: "a deadline set during this promotion is terminal, though the template runs the Bundle images",
			objs: []runtime.Object{stalledKs(web, fluxPushed, nil), stalledAt("web", after)}, since: since,
			want: isTerminal, reason: "(lastAttemptedRevision=034ce92a1b2c)"},
		{name: "a Deployment no longer past its deadline waits for Flux",
			objs: []runtime.Object{stalledKs(web, fluxPushed, nil), deploymentObj("web", 2, podinfo+":6.15.0", 1)}, since: since,
			want: isProgressing, reason: "set during this promotion: Deployment prod/web: Available=True, 1/1 replicas " +
				"updated and available; waiting for Flux to check again"},
		{name: "without a health-check start every deadline counts",
			objs: []runtime.Object{stalledKs(web, fluxPushed, nil), stalledAt("web", before)},
			want: isTerminal, reason: "stalled resources"},
		{name: "a listed Deployment that is gone is terminal",
			objs: []runtime.Object{stalledKs(web, fluxPushed, nil)}, since: since,
			want: isTerminal, reason: "stalled resources"},
		{name: "a listed resource of another kind is terminal",
			objs: []runtime.Object{stalledKs("[Deployment/prod/web status: 'Failed', StatefulSet/prod/db status: 'Failed']",
				fluxPushed, nil), stalledAt("web", before), stalledAt("db", before)}, since: since,
			want: isTerminal, reason: "StatefulSet/prod/db"},
		{name: "a second listed Deployment that stalled during this promotion is terminal",
			objs: []runtime.Object{stalledKs("[Deployment/prod/web status: 'Failed', Deployment/prod/api status: 'Failed']",
				fluxPushed, nil), stalledAt("web", before), stalledAt("api", after)}, since: since,
			want: isTerminal, reason: "Deployment/prod/api"},
		{name: "two listed Deployments from an earlier rollout wait for Flux",
			objs: []runtime.Object{stalledKs("[Deployment/prod/web status: 'Failed', Deployment/prod/api status: 'Failed']",
				fluxPushed, nil), stalledAt("web", before), stalledAt("api", before)}, since: since,
			want: isProgressing, reason: "Deployment prod/api: ProgressDeadlineExceeded"},
		{name: "a message that lists no resource is terminal",
			objs: []runtime.Object{stalledKs("[]", fluxPushed, nil), stalledAt("web", before)}, since: since,
			want: isTerminal, reason: "stalled resources"},
		{name: "a Kustomization that applies to another cluster is terminal",
			objs: []runtime.Object{stalledKs(web, fluxPushed, withSpec("kubeConfig",
				map[string]interface{}{"secretRef": map[string]interface{}{"name": "remote"}})), stalledAt("web", before)},
			since: since, want: isTerminal, reason: "stalled resources"},
		{name: "a sibling's commit with the Bundle Deployment's deadline from an earlier rollout waits for Flux",
			objs: []runtime.Object{stalledKs(web, fluxLater, nil), stalledAt("web", before)}, since: since,
			want: isProgressing, reason: "(lastAttemptedRevision=8e9966475a0b, not 034ce92a1b2c), but no Deployment Flux " +
				"lists is past a progress deadline set during this promotion: Deployment prod/web: ProgressDeadlineExceeded"},
		{name: "a sibling's commit with the Bundle Deployment no longer past its deadline waits for Flux",
			objs: []runtime.Object{stalledKs(web, fluxLater, nil), deploymentObj("web", 2, podinfo+":6.15.0", 1)}, since: since,
			want: isProgressing, reason: "Available=True, 1/1 replicas updated and available; waiting for Flux to check again"},
		{name: "a sibling's commit with a listed resource of another kind is unhealthy",
			objs: []runtime.Object{stalledKs("[Deployment/prod/web status: 'Failed', StatefulSet/prod/db status: 'Failed']",
				fluxLater, nil), stalledAt("web", before)}, since: since,
			want: isUnhealthy, reason: "Ready=False, observedGen=3, generation=3: health check failed"},
		{name: "a sibling's commit that timed out, not stalled, on an earlier deadline is unhealthy",
			objs: []runtime.Object{kustomization("False", "HealthCheckFailed",
				"health check failed after 3m0s: timeout waiting for: "+web, "main@sha1:"+fluxPrevious,
				both(withInventory("web"), func(obj map[string]interface{}) {
					obj["status"].(map[string]interface{})["lastAttemptedRevision"] = "main@sha1:" + fluxLater
				})), stalledAt("web", before)}, since: since,
			want: isUnhealthy, reason: "Ready=False, observedGen=3, generation=3: health check failed after 3m0s"},
		{name: "a sibling's commit without a health-check start is terminal",
			objs: []runtime.Object{stalledKs(web, fluxLater, nil), stalledAt("web", before)},
			want: isTerminal, reason: "but Deployment prod/web, which runs the Bundle images, stalled"},
		{name: "a sibling's commit with the Bundle Deployment stalled during this promotion is terminal",
			objs: []runtime.Object{stalledKs(web, fluxLater, nil), stalledAt("web", after)}, since: since,
			want: isTerminal, reason: "but Deployment prod/web, which runs the Bundle images, stalled"},
		{name: "reconciling the promoted commit again with a deadline from an earlier rollout waits",
			objs:  []runtime.Object{reconciling("main@sha1:"+fluxPushed, "main@sha1:"+fluxPushed, nil), stalledAt("web", before)},
			since: since, want: isProgressing, reason: "is from an earlier rollout"},
		{name: "reconciling the promoted commit again with a deadline set during this promotion is terminal",
			objs:  []runtime.Object{reconciling("main@sha1:"+fluxPushed, "main@sha1:"+fluxPushed, nil), stalledAt("web", after)},
			since: since, want: isTerminal, reason: "rollout failed: ProgressDeadlineExceeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkFlux(t, health.CheckOptions{ExpectedRevision: fluxPushed, ExpectedImages: bundle, Since: tt.since}, tt.objs...)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
		})
	}
}
