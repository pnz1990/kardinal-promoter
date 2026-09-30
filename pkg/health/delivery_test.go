// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"testing"

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
