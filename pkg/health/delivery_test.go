// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

const podinfo = "ghcr.io/stefanprodan/podinfo"

// The statuses below are trimmed from Rollouts that Argo Rollouts v1.10.0
// wrote on a kind cluster (the delivery spike of #1356).

// healthyRolloutStatus is a finished canary: stable ReplicaSet = current hash.
func healthyRolloutStatus(observed string) map[string]interface{} {
	return map[string]interface{}{
		"phase": "Healthy", "observedGeneration": observed,
		"stableRS": "5bc8ffd496", "currentPodHash": "5bc8ffd496",
		"replicas": int64(2), "updatedReplicas": int64(2), "availableReplicas": int64(2),
	}
}

// abortedRolloutStatus is a canary that Argo Rollouts aborted when the new
// ReplicaSet passed progressDeadlineSeconds (progressDeadlineAbort: true).
func abortedRolloutStatus(observed string) map[string]interface{} {
	return map[string]interface{}{
		"phase": "Degraded", "observedGeneration": observed, "abort": true,
		"message":  `RolloutAborted: Rollout aborted update to revision 6: ReplicaSet "ro2-c87f5d8cb" has timed out progressing.`,
		"stableRS": "85c4f78c47", "currentPodHash": "c87f5d8cb",
		"replicas": int64(2), "availableReplicas": int64(2),
	}
}

func rolloutObj(generation int64, spec, status map[string]interface{}) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout",
		"metadata": map[string]interface{}{"name": "web", "namespace": "prod", "generation": generation},
		"spec":     spec,
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

// podSpec is a pod template spec running image.
func podSpec(image string) map[string]interface{} {
	return map[string]interface{}{"spec": map[string]interface{}{"containers": []interface{}{
		map[string]interface{}{"name": "podinfo", "image": image},
	}}}
}

func templateSpec(image string) map[string]interface{} {
	return map[string]interface{}{"replicas": int64(2), "template": podSpec(image)}
}

// deploymentObj is a finished Deployment rollout of image at generation.
func deploymentObj(name string, generation int64, image string, replicas int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]interface{}{"name": name, "namespace": "prod", "generation": generation},
		"spec":     map[string]interface{}{"replicas": replicas, "template": podSpec(image)},
		"status": map[string]interface{}{
			"observedGeneration": generation, "replicas": replicas, "updatedReplicas": replicas,
			"readyReplicas": replicas, "availableReplicas": replicas,
			"conditions": []interface{}{
				map[string]interface{}{"type": "Available", "status": "True", "reason": "MinimumReplicasAvailable"},
				map[string]interface{}{"type": "Progressing", "status": "True", "reason": "NewReplicaSetAvailable"},
			},
		},
	}}
}

// TestArgoRolloutsAdapter_Revision proves bugs 1 and 9 of the delivery spike
// fixed: the Rollout phase decides health only once the Rollout runs the
// Bundle images and Argo Rollouts observed that spec. A Healthy or Degraded
// phase left from the previous revision is Waiting.
func TestArgoRolloutsAdapter_Revision(t *testing.T) {
	bundle := []health.ImageExpectation{{Repository: podinfo, Tag: "6.15.0"}}
	workloadRef := map[string]interface{}{"workloadRef": map[string]interface{}{
		"apiVersion": "apps/v1", "kind": "Deployment", "name": "web"}}
	withWorkloadGen := func(st map[string]interface{}, gen string) map[string]interface{} {
		st["workloadObservedGeneration"] = gen
		return st
	}
	tests := []struct {
		name     string
		objs     []runtime.Object
		expected []health.ImageExpectation
		want     wantKind
		reason   string
	}{
		{name: "Healthy on the Bundle image",
			objs:     []runtime.Object{rolloutObj(4, templateSpec(podinfo+":6.15.0"), healthyRolloutStatus("4"))},
			expected: bundle, want: isHealthy, reason: "Rollout phase: Healthy"},
		{name: "stale Healthy: the GitOps tool has not applied the Bundle yet (bug 1)",
			objs:     []runtime.Object{rolloutObj(4, templateSpec(podinfo+":6.14.0"), healthyRolloutStatus("4"))},
			expected: bundle, want: isProgressing,
			reason: "Rollout prod/web not updated yet: runs " + podinfo + ":6.14.0, Bundle has " + podinfo + ":6.15.0"},
		{name: "stale Healthy: Argo Rollouts has not observed the new spec",
			objs:     []runtime.Object{rolloutObj(5, templateSpec(podinfo+":6.15.0"), healthyRolloutStatus("4"))},
			expected: bundle, want: isProgressing,
			reason: "waiting for the Argo Rollouts controller to observe generation 5 (observed 4"},
		{name: "Healthy but the stable ReplicaSet is not the current one",
			objs: []runtime.Object{rolloutObj(4, templateSpec(podinfo+":6.15.0"), func() map[string]interface{} {
				st := healthyRolloutStatus("4")
				st["stableRS"] = "85c4f78c47"
				return st
			}())},
			expected: bundle, want: isProgressing, reason: `stable ReplicaSet "85c4f78c47" is not the current pod template hash "5bc8ffd496"`},
		{name: "no observedGeneration",
			objs:     []runtime.Object{rolloutObj(1, templateSpec(podinfo+":6.15.0"), map[string]interface{}{"phase": "Healthy"})},
			expected: bundle, want: isProgressing, reason: "status.observedGeneration not set"},
		{name: "stale Degraded of the previous, broken release is Waiting, not a failure (bug 9)",
			objs:     []runtime.Object{rolloutObj(6, templateSpec(podinfo+":0.0.0-broken"), abortedRolloutStatus("6"))},
			expected: bundle, want: isProgressing, reason: "not updated yet: runs " + podinfo + ":0.0.0-broken"},
		{name: "Degraded on the Bundle image",
			objs:     []runtime.Object{rolloutObj(6, templateSpec(podinfo+":6.15.0"), abortedRolloutStatus("6"))},
			expected: bundle, want: isUnhealthy, reason: "Rollout phase: Degraded — RolloutAborted: Rollout aborted update"},
		{name: "Paused on the Bundle image",
			objs: []runtime.Object{rolloutObj(4, templateSpec(podinfo+":6.15.0"), map[string]interface{}{
				"phase": "Paused", "message": "CanaryPauseStep", "observedGeneration": "4"})},
			expected: bundle, want: isProgressing, reason: "Rollout phase: Paused — CanaryPauseStep"},
		{name: "a Bundle without images: phase and generation decide",
			objs: []runtime.Object{rolloutObj(4, templateSpec(podinfo+":6.14.0"), healthyRolloutStatus("4"))},
			want: isHealthy, reason: "Rollout phase: Healthy"},
		{name: "a template without the Bundle repository cannot be verified",
			objs:     []runtime.Object{rolloutObj(4, templateSpec("registry.local/podinfo:6.15.0"), healthyRolloutStatus("4"))},
			expected: bundle, want: isHealthy, reason: "(image not verified: runs none of the Bundle images)"},
		{name: "workloadRef Deployment on the Bundle image",
			objs: []runtime.Object{rolloutObj(2, workloadRef, withWorkloadGen(healthyRolloutStatus("2"), "3")),
				deploymentObj("web", 3, podinfo+":6.15.0", 0)},
			expected: bundle, want: isHealthy},
		{name: "workloadRef Deployment on the previous image",
			objs: []runtime.Object{rolloutObj(2, workloadRef, withWorkloadGen(healthyRolloutStatus("2"), "3")),
				deploymentObj("web", 3, podinfo+":6.14.0", 0)},
			expected: bundle, want: isProgressing, reason: "not updated yet: runs " + podinfo + ":6.14.0"},
		{name: "workloadRef Deployment generation not observed",
			objs: []runtime.Object{rolloutObj(2, workloadRef, withWorkloadGen(healthyRolloutStatus("2"), "3")),
				deploymentObj("web", 4, podinfo+":6.15.0", 0)},
			expected: bundle, want: isProgressing, reason: "observe generation 4 of Deployment prod/web"},
		{name: "workloadRef Deployment missing",
			objs:     []runtime.Object{rolloutObj(2, workloadRef, withWorkloadGen(healthyRolloutStatus("2"), "3"))},
			expected: bundle, want: isUnhealthy, reason: "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dyn := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), tt.objs...)
			got, err := health.NewArgoRolloutsAdapter(dyn).Check(context.Background(), health.CheckOptions{
				ArgoRollouts:   health.ArgoRolloutsConfig{Name: "web", Namespace: "prod"},
				ExpectedImages: tt.expected,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
		})
	}
}

// canaryObj is Canary prod/web targeting Deployment prod/web, trimmed from
// the Canary that Flagger v1.45.0 wrote in the delivery spike. Flagger set
// phase at lastTransition: status.lastTransitionTime and, with a message, the
// Promoted condition's times. A nil targetRef leaves spec.targetRef out.
func canaryObj(targetRef map[string]interface{}, phase, lastTransition, message string) *unstructured.Unstructured {
	spec := map[string]interface{}{
		"provider": "kubernetes", "progressDeadlineSeconds": int64(60),
		"service":  map[string]interface{}{"port": int64(9898), "targetPort": int64(9898)},
		"analysis": map[string]interface{}{"interval": "10s", "iterations": int64(2), "threshold": int64(2)},
	}
	if targetRef != nil {
		spec["targetRef"] = targetRef
	}
	status := map[string]interface{}{"phase": phase}
	if lastTransition != "" {
		status["lastTransitionTime"] = lastTransition
	}
	if message != "" {
		cond := map[string]interface{}{"type": "Promoted", "status": "True", "reason": phase, "message": message}
		if lastTransition != "" {
			cond["lastUpdateTime"], cond["lastTransitionTime"] = lastTransition, lastTransition
		}
		status["conditions"] = []interface{}{cond}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "flagger.app/v1beta1", "kind": "Canary",
		"metadata": map[string]interface{}{"name": "web", "namespace": "prod", "generation": int64(1)},
		"spec":     spec, "status": status,
	}}
}

// TestFlaggerAdapter_Revision proves bugs 2 and 3 of the delivery spike fixed:
// Flagger keeps the phase of its last analysis until it notices the new
// revision, so a Succeeded or Failed phase counts only once it is about the
// Bundle: the primary Deployment runs the Bundle images, or (when images can't
// be compared) Flagger set the phase after the health check started.
func TestFlaggerAdapter_Revision(t *testing.T) {
	bundle := []health.ImageExpectation{{Repository: podinfo, Tag: "6.15.0"}}
	target := map[string]interface{}{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web"}
	since := time.Date(2026, 9, 30, 22, 0, 0, 0, time.UTC)
	before, after := "2026-09-30T21:56:24Z", "2026-09-30T22:03:10Z"
	const (
		okMsg   = "Canary analysis completed successfully, promotion finished."
		failMsg = "Canary analysis failed, Deployment scaled to zero."
	)
	// Flagger scales the target to zero after an analysis; the primary serves.
	targetOn := func(image string) *unstructured.Unstructured { return deploymentObj("web", 14, image, 0) }
	primaryOn := func(image string) *unstructured.Unstructured { return deploymentObj("web-primary", 4, image, 1) }
	rollingPrimary := func(image string) *unstructured.Unstructured {
		d := primaryOn(image)
		d.Object["status"].(map[string]interface{})["updatedReplicas"] = int64(0)
		return d
	}
	// ticked is c after a later analysis tick: Flagger rewrites
	// status.lastTransitionTime of a Failed Canary at every tick, and leaves
	// its Promoted condition alone.
	ticked := func(c *unstructured.Unstructured, at string) *unstructured.Unstructured {
		c.Object["status"].(map[string]interface{})["lastTransitionTime"] = at
		return c
	}
	// promotedFor sets c's Promoted condition to the one of phase, set at.
	promotedFor := func(c *unstructured.Unstructured, phase, at string) *unstructured.Unstructured {
		c.Object["status"].(map[string]interface{})["conditions"] = []interface{}{map[string]interface{}{
			"type": "Promoted", "status": "Unknown", "reason": phase, "lastUpdateTime": at,
			"message": "New revision detected, progressing canary analysis."}}
		return c
	}
	tests := []struct {
		name     string
		objs     []runtime.Object
		expected []health.ImageExpectation
		since    time.Time
		want     wantKind
		reason   string
	}{
		{name: "Succeeded once the primary runs the Bundle images",
			objs:     []runtime.Object{canaryObj(target, "Succeeded", after, okMsg), targetOn(podinfo + ":6.15.0"), primaryOn(podinfo + ":6.15.0")},
			expected: bundle, since: since, want: isHealthy,
			reason: "Canary phase: Succeeded; primary Deployment prod/web-primary runs the Bundle images"},
		{name: "stale Succeeded of the previous release before Flagger analyzes the Bundle (bug 2)",
			objs:     []runtime.Object{canaryObj(target, "Succeeded", before, okMsg), targetOn(podinfo + ":0.0.0-e2e-missing"), primaryOn(podinfo + ":6.14.1")},
			expected: []health.ImageExpectation{{Repository: podinfo, Tag: "0.0.0-e2e-missing"}}, since: since, want: isProgressing,
			reason: "Canary phase: Succeeded is for an earlier release: primary Deployment prod/web-primary runs " + podinfo + ":6.14.1"},
		{name: "stale Succeeded with no health-check start known still needs the primary on the Bundle",
			objs:     []runtime.Object{canaryObj(target, "Succeeded", before, okMsg), targetOn(podinfo + ":6.15.0"), primaryOn(podinfo + ":6.14.1")},
			expected: bundle, want: isProgressing, reason: "Succeeded is for an earlier release"},
		{name: "the GitOps tool has not updated the target yet",
			objs:     []runtime.Object{canaryObj(target, "Succeeded", before, okMsg), targetOn(podinfo + ":6.14.1"), primaryOn(podinfo + ":6.14.1")},
			expected: bundle, since: since, want: isProgressing,
			reason: "Canary prod/web: target Deployment prod/web not updated yet: runs " + podinfo + ":6.14.1"},
		{name: "Succeeded while the primary still rolls out the Bundle",
			objs:     []runtime.Object{canaryObj(target, "Succeeded", after, okMsg), targetOn(podinfo + ":6.15.0"), rollingPrimary(podinfo + ":6.15.0")},
			expected: bundle, since: since, want: isProgressing, reason: "Deployment prod/web-primary rolling out"},
		{name: "stale Failed of the previous release does not fail the Bundle (bug 3)",
			objs:     []runtime.Object{canaryObj(target, "Failed", before, failMsg), targetOn(podinfo + ":6.15.0"), primaryOn(podinfo + ":6.14.1")},
			expected: bundle, since: since, want: isProgressing,
			reason: "Canary phase: Failed is for an earlier release: its Promoted condition's lastUpdateTime 2026-09-30T21:56:24Z is before this health check started (2026-09-30T22:00:00Z)"},
		{name: "stale Failed that a later Flagger tick re-stamped does not fail the Bundle (bug 3, seen live)",
			objs:     []runtime.Object{ticked(canaryObj(target, "Failed", before, failMsg), after), targetOn(podinfo + ":6.15.0"), primaryOn(podinfo + ":6.14.1")},
			expected: bundle, since: since, want: isProgressing,
			reason: "Canary phase: Failed is for an earlier release: its Promoted condition's lastUpdateTime 2026-09-30T21:56:24Z is before"},
		{name: "Failed after the health check started is terminal with Flagger's reason",
			objs:     []runtime.Object{canaryObj(target, "Failed", after, failMsg), targetOn(podinfo + ":6.15.0"), primaryOn(podinfo + ":6.14.1")},
			expected: bundle, since: since, want: isTerminal, reason: "Canary phase: Failed — " + failMsg},
		{name: "Failed with no health-check start known is terminal",
			objs:     []runtime.Object{canaryObj(target, "Failed", before, failMsg), targetOn(podinfo + ":6.15.0"), primaryOn(podinfo + ":6.14.1")},
			expected: bundle, want: isTerminal, reason: failMsg},
		{name: "Failed while the Bundle is the revision Flagger last promoted (rolled back by hand)",
			objs:     []runtime.Object{canaryObj(target, "Failed", before, failMsg), targetOn(podinfo + ":6.15.0"), primaryOn(podinfo + ":6.15.0")},
			expected: bundle, since: since, want: isHealthy, reason: "the Bundle is the revision Flagger last promoted"},
		{name: "Progressing is Waiting",
			objs:     []runtime.Object{canaryObj(target, "Progressing", after, "New revision detected"), targetOn(podinfo + ":6.15.0"), primaryOn(podinfo + ":6.14.1")},
			expected: bundle, since: since, want: isProgressing, reason: "Canary phase: Progressing"},
		{name: "target Deployment missing",
			objs:     []runtime.Object{canaryObj(target, "Succeeded", after, okMsg)},
			expected: bundle, since: since, want: isUnhealthy, reason: "target Deployment prod/web not found"},
		{name: "without Bundle images a Succeeded set before the health check started is stale",
			objs:  []runtime.Object{canaryObj(target, "Succeeded", before, okMsg), targetOn(podinfo + ":6.14.1"), primaryOn(podinfo + ":6.14.1")},
			since: since, want: isProgressing, reason: "Succeeded is for an earlier release: its Promoted condition's lastUpdateTime"},
		{name: "without a Promoted condition the phase's time is status.lastTransitionTime",
			objs:  []runtime.Object{canaryObj(nil, "Succeeded", before, "")},
			since: since, want: isProgressing, reason: "its status.lastTransitionTime 2026-09-30T21:56:24Z is before"},
		{name: "a Promoted condition of another phase does not date the phase",
			objs:  []runtime.Object{promotedFor(canaryObj(nil, "Succeeded", after, ""), "Progressing", before)},
			since: since, want: isHealthy, reason: "Canary phase: Succeeded"},
		{name: "without Bundle images a Succeeded set after the health check started counts",
			objs:  []runtime.Object{canaryObj(target, "Succeeded", after, okMsg), targetOn(podinfo + ":6.14.1"), primaryOn(podinfo + ":6.14.1")},
			since: since, want: isHealthy, reason: "Canary phase: Succeeded"},
		{name: "a lastTransitionTime in the same second as the health-check start counts",
			objs:  []runtime.Object{canaryObj(target, "Succeeded", "2026-09-30T22:00:00Z", okMsg)},
			since: since.Add(400 * time.Millisecond), want: isHealthy},
		{name: "a target without the Bundle repository falls back to the time check",
			objs:     []runtime.Object{canaryObj(target, "Succeeded", before, okMsg), targetOn("registry.local/podinfo:6.15.0"), primaryOn("registry.local/podinfo:6.15.0")},
			expected: bundle, since: since, want: isProgressing, reason: "its Promoted condition's lastUpdateTime"},
		{name: "no targetRef: a fresh Succeeded counts, image not verified",
			objs:     []runtime.Object{canaryObj(nil, "Succeeded", after, okMsg)},
			expected: bundle, since: since, want: isHealthy, reason: "(image not verified: the Canary has no spec.targetRef)"},
		{name: "no lastTransitionTime: Succeeded is not trusted",
			objs:  []runtime.Object{canaryObj(nil, "Succeeded", "", okMsg)},
			since: since, want: isProgressing, reason: `status.lastTransitionTime "" is not a time`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dyn := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), tt.objs...)
			got, err := health.NewFlaggerAdapter(dyn).Check(context.Background(), health.CheckOptions{
				Flagger:        health.FlaggerConfig{Name: "web", Namespace: "prod"},
				ExpectedImages: tt.expected,
				Since:          tt.since,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
		})
	}
}
