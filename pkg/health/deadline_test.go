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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
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
			got, err := health.NewDeploymentAdapter(c, nil).Check(context.Background(), health.CheckOptions{
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

// replicaSetObj is ReplicaSet prod/<name> at revision (none when empty).
func replicaSetObj(name, revision string) *unstructured.Unstructured {
	rs := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "apps/v1", "kind": "ReplicaSet",
		"metadata": map[string]interface{}{"name": name, "namespace": "prod"}}}
	if revision != "" {
		rs.SetAnnotations(map[string]string{"deployment.kubernetes.io/revision": revision})
	}
	return rs
}

// TestDeploymentDeadlineOfReplicaSet proves B95 fixed: the condition's times
// can take this rollout's ProgressDeadlineExceeded for an earlier one. With
// health.labelSelector, while a second matching Deployment does not run the
// Bundle images yet, no check records the template update
// (status.targetUpdatedAt), so the stalled Deployment's deadline waited for
// health.timeout; so did a deadline the first check found already set (a
// late merge, a short progressDeadlineSeconds). The ReplicaSet the condition
// names now decides: one whose revision is the Deployment's is the stall of
// the current pod template, whatever the times, and one of another revision,
// or one that is gone, is from an earlier rollout. When the ReplicaSet cannot
// tell, the times decide as before.
func TestDeploymentDeadlineOfReplicaSet(t *testing.T) {
	v2 := []health.ImageExpectation{{Repository: "ghcr.io/org/app", Tag: "v2"}}
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		return v
	}
	const stalled = `ReplicaSet "web-5d8f" has timed out progressing.`
	since := at("2026-10-02T05:00:00Z")
	const firstCheck = "is from an earlier rollout: this check is the first to find the pod template running the Bundle images"
	tests := []struct {
		name       string
		revision   string
		message    string
		deadlineAt string
		since      time.Time
		seen       time.Time
		replicaSet *unstructured.Unstructured
		forbidden  bool
		second     bool
		// image is the pod template's image (the Bundle's when empty);
		// noImages is a Bundle without images (type config), and mixed a
		// Bundle that is not only images.
		image    string
		noImages bool
		mixed    bool
		want     wantKind
		reason   string
	}{
		{name: "the current ReplicaSet's deadline from before the health check started fails at once",
			revision: "3", deadlineAt: "2026-10-02T04:59:00Z", since: since, replicaSet: replicaSetObj("web-5d8f", "3"),
			want: isTerminal, reason: "Deployment prod/web rollout failed: ProgressDeadlineExceeded: " + stalled},
		{name: "the current ReplicaSet's deadline the first check found already set fails at once",
			revision: "3", deadlineAt: "2026-10-02T05:01:30Z", since: since, seen: at("2026-10-02T05:02:00Z"),
			replicaSet: replicaSetObj("web-5d8f", "3"), want: isTerminal, reason: "rollout failed: ProgressDeadlineExceeded"},
		{name: "the current ReplicaSet's deadline fails the first check to find the Bundle template",
			revision: "3", deadlineAt: "2026-10-02T05:01:30Z", since: since, replicaSet: replicaSetObj("web-5d8f", "3"),
			want: isTerminal, reason: "rollout failed: ProgressDeadlineExceeded"},
		{name: "one matching Deployment stalled while another does not run the Bundle images fails at once",
			revision: "3", deadlineAt: "2026-10-02T05:01:30Z", since: since, replicaSet: replicaSetObj("web-5d8f", "3"),
			second: true, want: isTerminal, reason: "Deployment prod/web rollout failed: ProgressDeadlineExceeded: " + stalled},
		{name: "a ReplicaSet of another revision is an earlier rollout's, also after the template update",
			revision: "3", deadlineAt: "2026-10-02T05:01:30Z", since: since, seen: at("2026-10-02T05:01:00Z"),
			replicaSet: replicaSetObj("web-5d8f", "2"), want: isProgressing,
			reason: "Deployment prod/web: ProgressDeadlineExceeded (" + stalled + ") is from an earlier rollout: " +
				"ReplicaSet web-5d8f has revision 2, not the Deployment's revision 3; " +
				"waiting for the Deployment controller to see this rollout progress"},
		{name: "a ReplicaSet of another revision is an earlier rollout's without a health-check start",
			revision: "3", deadlineAt: "2026-10-02T05:01:30Z", replicaSet: replicaSetObj("web-5d8f", "2"),
			want: isProgressing, reason: "ReplicaSet web-5d8f has revision 2, not the Deployment's revision 3"},
		{name: "a ReplicaSet that is gone is an earlier rollout's",
			revision: "3", deadlineAt: "2026-10-02T05:01:30Z", since: since, seen: at("2026-10-02T05:01:00Z"),
			want: isProgressing, reason: "is from an earlier rollout: ReplicaSet web-5d8f no longer exists; waiting"},
		{name: "a ReplicaSet without a revision leaves the times to decide",
			revision: "3", deadlineAt: "2026-10-02T05:01:30Z", since: since, replicaSet: replicaSetObj("web-5d8f", ""),
			want: isProgressing, reason: firstCheck},
		{name: "a Deployment without a revision leaves the times to decide",
			deadlineAt: "2026-10-02T05:01:30Z", since: since, replicaSet: replicaSetObj("web-5d8f", "3"),
			want: isProgressing, reason: firstCheck},
		{name: "a message that names no ReplicaSet leaves the times to decide",
			revision: "3", message: `Deployment "web" has timed out progressing.`, deadlineAt: "2026-10-02T05:01:30Z",
			since: since, replicaSet: replicaSetObj("web-5d8f", "3"), want: isProgressing, reason: firstCheck},
		{name: "another message that names a ReplicaSet leaves the times to decide",
			revision: "3", message: `ReplicaSet "web-5d8f" is progressing.`, deadlineAt: "2026-10-02T05:01:30Z",
			since: since, replicaSet: replicaSetObj("web-5d8f", "3"), want: isProgressing, reason: firstCheck},
		{name: "a ReplicaSet that cannot be read leaves the times to decide",
			revision: "3", deadlineAt: "2026-10-02T05:01:30Z", since: since, replicaSet: replicaSetObj("web-5d8f", "3"),
			forbidden: true, want: isProgressing, reason: firstCheck},
		{name: "a ReplicaSet name with dots is read",
			revision: "3", message: `ReplicaSet "web.v2-5d8f" has timed out progressing.`, deadlineAt: "2026-10-02T04:59:00Z",
			since: since, replicaSet: replicaSetObj("web.v2-5d8f", "3"), want: isTerminal, reason: "rollout failed"},
		{name: "a message with more text after the ReplicaSet leaves the times to decide",
			revision: "3", message: stalled + " Check the pods.", deadlineAt: "2026-10-02T05:01:30Z",
			since: since, replicaSet: replicaSetObj("web-5d8f", "3"), want: isProgressing, reason: firstCheck},
		{name: "a message with more text before the ReplicaSet leaves the times to decide",
			revision: "3", message: "Deployment web: " + stalled, deadlineAt: "2026-10-02T05:01:30Z",
			since: since, replicaSet: replicaSetObj("web-5d8f", "3"), want: isProgressing, reason: firstCheck},
		// The rollback of a broken config change: the check before Argo CD
		// applies it finds the broken release's stall on the current
		// ReplicaSet (B95 review).
		{name: "the current ReplicaSet's deadline from before the health check started waits for a config Bundle",
			revision: "3", deadlineAt: "2026-10-02T04:59:00Z", since: since, replicaSet: replicaSetObj("web-5d8f", "3"),
			noImages: true, want: isProgressing, reason: "Deployment prod/web: ProgressDeadlineExceeded (" + stalled +
				") is from an earlier rollout: its lastUpdateTime 2026-10-02T04:59:00Z is before this health check started"},
		{name: "the current ReplicaSet's deadline set during the health check fails a config Bundle",
			revision: "3", deadlineAt: "2026-10-02T05:01:30Z", since: since, replicaSet: replicaSetObj("web-5d8f", "3"),
			noImages: true, want: isTerminal, reason: "rollout failed: ProgressDeadlineExceeded: " + stalled},
		{name: "a ReplicaSet of another revision is an earlier rollout's for a config Bundle",
			revision: "3", deadlineAt: "2026-10-02T05:01:30Z", since: since, replicaSet: replicaSetObj("web-5d8f", "2"),
			noImages: true, want: isProgressing, reason: "ReplicaSet web-5d8f has revision 2, not the Deployment's revision 3"},
		{name: "the current ReplicaSet's deadline from before the health check started waits for a mixed Bundle on its images",
			revision: "3", deadlineAt: "2026-10-02T04:59:00Z", since: since, replicaSet: replicaSetObj("web-5d8f", "3"),
			mixed: true, want: isProgressing, reason: "is before this health check started"},
		{name: "the current ReplicaSet's deadline after the template update fails a mixed Bundle",
			revision: "3", deadlineAt: "2026-10-02T05:01:30Z", since: since, seen: at("2026-10-02T05:01:00Z"),
			replicaSet: replicaSetObj("web-5d8f", "3"), mixed: true, want: isTerminal, reason: "rollout failed"},
		{name: "the current ReplicaSet's deadline waits while the image cannot be verified",
			revision: "3", deadlineAt: "2026-10-02T04:59:00Z", since: since, replicaSet: replicaSetObj("web-5d8f", "3"),
			image: "registry.local:5000/other:v9", want: isProgressing, reason: "is before this health check started"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := stalled
			if tt.message != "" {
				msg = tt.message
			}
			image := "ghcr.io/org/app:v2"
			if tt.image != "" {
				image = tt.image
			}
			d := deployment("web", image, func(d *appsv1.Deployment) {
				if tt.revision != "" {
					d.Annotations = map[string]string{"deployment.kubernetes.io/revision": tt.revision}
				}
				d.Status.Replicas, d.Status.UnavailableReplicas = 3, 1
				setProgressing(d, corev1.ConditionFalse, "ProgressDeadlineExceeded")
				d.Status.Conditions[1].Message = msg
				d.Status.Conditions[1].LastUpdateTime = metav1.NewTime(at(tt.deadlineAt))
			})
			b := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(d)
			opts := health.CheckOptions{Resource: health.ResourceConfig{Name: "web", Namespace: "prod"},
				ExpectedImages: v2, ImagesOnly: !tt.noImages && !tt.mixed, Since: tt.since, TargetUpdatedAt: tt.seen}
			if tt.noImages {
				opts.ExpectedImages = nil
			}
			if tt.second {
				b = b.WithObjects(deployment("web-canary", "ghcr.io/org/app:v1", nil))
				opts.Resource.LabelSelector = map[string]string{"app": "web"}
			}
			var objs []runtime.Object
			if tt.replicaSet != nil {
				objs = append(objs, tt.replicaSet)
			}
			dyn := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), objs...)
			if tt.forbidden {
				dyn.PrependReactor("get", "replicasets", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "replicasets"}, "web-5d8f", nil)
				})
			}
			got, err := health.NewDeploymentAdapter(b.Build(), dyn).Check(context.Background(), opts)
			require.NoError(t, err)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
			assert.Equal(t, !tt.second && !tt.noImages && tt.image == "", got.TargetUpdated, "TargetUpdated")
		})
	}
}

// TestAutoDetector_ResourceReadsReplicaSets proves the resource adapter that
// the controller selects reads the ReplicaSet a ProgressDeadlineExceeded
// names through the dynamic client (B95).
func TestAutoDetector_ResourceReadsReplicaSets(t *testing.T) {
	d := deployment("web", "ghcr.io/org/app:v2", func(d *appsv1.Deployment) {
		d.Annotations = map[string]string{"deployment.kubernetes.io/revision": "3"}
		setProgressing(d, corev1.ConditionFalse, "ProgressDeadlineExceeded")
		d.Status.Conditions[1].Message = `ReplicaSet "web-5d8f" has timed out progressing.`
	})
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(d).Build()
	dyn := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), replicaSetObj("web-5d8f", "2"))
	adapter, err := health.NewAutoDetector(c, dyn).Select(context.Background(), "resource")
	require.NoError(t, err)
	got, err := adapter.Check(context.Background(), health.CheckOptions{
		Resource: health.ResourceConfig{Name: "web", Namespace: "prod"}})
	require.NoError(t, err)
	assert.Equal(t, isProgressing, kindOf(got), got.Reason)
	assert.Contains(t, got.Reason, "ReplicaSet web-5d8f has revision 2, not the Deployment's revision 3")
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
			got, err := health.NewDeploymentAdapter(b.Build(), nil).Check(context.Background(), opts)
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
	stalledOn := func(name, image, at string) *unstructured.Unstructured {
		d := stalledDeployment(name, image)
		c := d.Object["status"].(map[string]interface{})["conditions"].([]interface{})[1].(map[string]interface{})
		c["lastUpdateTime"], c["lastTransitionTime"] = at, at
		c["message"] = `ReplicaSet "` + name + `-5d8f" has timed out progressing.`
		return d
	}
	stalledAt := func(name, at string) *unstructured.Unstructured { return stalledOn(name, podinfo+":6.15.0", at) }
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
		"of this promotion's rollout: Deployment prod/web: ProgressDeadlineExceeded (ReplicaSet \"web-5d8f\" has timed out " +
		"progressing.) is from an earlier rollout: its lastUpdateTime 2026-10-02T05:01:30Z is before this health check " +
		"started (2026-10-02T05:02:00Z); waiting for the Deployment controller to see this rollout progress; " +
		"waiting for Flux to check again"
	// atRevision is d with the Deployment controller's revision.
	atRevision := func(d *unstructured.Unstructured, revision string) *unstructured.Unstructured {
		d.SetAnnotations(map[string]string{"deployment.kubernetes.io/revision": revision})
		return d
	}
	tests := []struct {
		name  string
		objs  []runtime.Object
		since time.Time
		// mixed is a Bundle that is not only images.
		mixed  bool
		want   wantKind
		reason string
	}{
		{name: "a deadline set during this promotion on a ReplicaSet of another revision waits for Flux",
			objs: []runtime.Object{stalledKs(web, fluxPushed, nil), atRevision(stalledAt("web", after), "3"),
				replicaSetObj("web-5d8f", "2")}, since: since,
			want: isProgressing, reason: "(lastAttemptedRevision=034ce92a1b2c), but no Deployment Flux lists is past a " +
				"progress deadline of this promotion's rollout: Deployment prod/web: ProgressDeadlineExceeded (ReplicaSet " +
				"\"web-5d8f\" has timed out progressing.) is from an earlier rollout: ReplicaSet web-5d8f has revision 2, " +
				"not the Deployment's revision 3; waiting for the Deployment controller to see this rollout progress; " +
				"waiting for Flux to check again"},
		{name: "a deadline from before the health check started on the current ReplicaSet is terminal",
			objs: []runtime.Object{stalledKs(web, fluxPushed, nil), atRevision(stalledAt("web", before), "3"),
				replicaSetObj("web-5d8f", "3")}, since: since,
			want: isTerminal, reason: "(lastAttemptedRevision=034ce92a1b2c)"},
		{name: "a deadline from before the health check started on the current ReplicaSet waits for a mixed Bundle",
			objs: []runtime.Object{stalledKs(web, fluxPushed, nil), atRevision(stalledAt("web", before), "3"),
				replicaSetObj("web-5d8f", "3")}, since: since, mixed: true,
			want: isProgressing, reason: earlier},
		{name: "a deadline from before the health check started waits for Flux",
			objs: []runtime.Object{stalledKs(web, fluxPushed, nil), stalledAt("web", before)}, since: since,
			want: isProgressing, reason: earlier},
		{name: "a deadline from before the health check started on the current ReplicaSet of a Deployment on no Bundle image waits",
			objs: []runtime.Object{stalledKs("[Deployment/prod/cache status: 'Failed']", fluxPushed, nil),
				atRevision(stalledOn("cache", "docker.io/library/redis:7", before), "3"), replicaSetObj("cache-5d8f", "3")},
			since: since, want: isProgressing, reason: "Deployment prod/cache: ProgressDeadlineExceeded (ReplicaSet " +
				"\"cache-5d8f\" has timed out progressing.) is from an earlier rollout: its lastUpdateTime 2026-10-02T05:01:30Z " +
				"is before this health check started"},
		{name: "a deadline set during this promotion is terminal, though the template runs the Bundle images",
			objs: []runtime.Object{stalledKs(web, fluxPushed, nil), stalledAt("web", after)}, since: since,
			want: isTerminal, reason: "(lastAttemptedRevision=034ce92a1b2c)"},
		{name: "a Deployment no longer past its deadline waits for Flux",
			objs: []runtime.Object{stalledKs(web, fluxPushed, nil), deploymentObj("web", 2, podinfo+":6.15.0", 1)}, since: since,
			want: isProgressing, reason: "of this promotion's rollout: Deployment prod/web: Available=True, 1/1 replicas " +
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
				"lists is past a progress deadline of this promotion's rollout: Deployment prod/web: ProgressDeadlineExceeded"},
		{name: "a sibling's commit with the Bundle Deployment's current ReplicaSet stalled before the health check is terminal",
			objs: []runtime.Object{stalledKs(web, fluxLater, nil), atRevision(stalledAt("web", before), "3"),
				replicaSetObj("web-5d8f", "3")}, since: since,
			want: isTerminal, reason: "but Deployment prod/web, which runs the Bundle images, stalled"},
		// A mixed Bundle whose images the Deployment already runs: Flux has
		// not applied its config change (B95 review).
		{name: "a sibling's commit with the current ReplicaSet stalled before the health check waits for a mixed Bundle",
			objs: []runtime.Object{stalledKs(web, fluxLater, nil), atRevision(stalledAt("web", before), "3"),
				replicaSetObj("web-5d8f", "3")}, since: since, mixed: true,
			want: isProgressing, reason: "(lastAttemptedRevision=8e9966475a0b, not 034ce92a1b2c), but no Deployment Flux " +
				"lists is past a progress deadline of this promotion's rollout: Deployment prod/web: ProgressDeadlineExceeded " +
				"(ReplicaSet \"web-5d8f\" has timed out progressing.) is from an earlier rollout: its lastUpdateTime " +
				"2026-10-02T05:01:30Z is before this health check started"},
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
			got := checkFlux(t, health.CheckOptions{ExpectedRevision: fluxPushed, ExpectedImages: bundle,
				ImagesOnly: !tt.mixed, Since: tt.since}, tt.objs...)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
		})
	}
}
