// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// stuckRollout is a one-replica rolling update whose new pod (ReplicaSet
// web-7d9f) never becomes available: the old pod stays.
func stuckRollout() *appsv1.Deployment {
	return deployment("web", "ghcr.io/org/app:v2", func(d *appsv1.Deployment) {
		one := int32(1)
		d.Spec.Replicas = &one
		d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}
		d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.ReadyReplicas = 2, 1, 1
		d.Status.AvailableReplicas, d.Status.UnavailableReplicas = 1, 1
		setProgressing(d, corev1.ConditionTrue, "ReplicaSetUpdated")
		d.Status.Conditions[1].Message = `ReplicaSet "web-7d9f" is progressing.`
	})
}

func pod(name, hash string, statuses ...corev1.ContainerStatus) *unstructured.Unstructured {
	p := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", Labels: map[string]string{"app": "web", "pod-template-hash": hash}},
		Status:     corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: statuses},
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(p)
	if err != nil {
		panic(err)
	}
	return &unstructured.Unstructured{Object: obj}
}

func waiting(name, reason, msg string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: msg}}}
}

func running(name string, ready bool) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, Ready: ready, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
}

// TestDeploymentAdapter_NamesNewPodFailure proves #1365 item 6: while a
// rolling update waits for a new pod that never becomes available, the
// resource adapter's reason (and so the health.timeout message, which quotes
// the last result) names the new pod's failure: the waiting reason of its
// first unready container. Pods of the old ReplicaSet do not count, and the
// reason stays as before when the pods cannot be read.
func TestDeploymentAdapter_NamesNewPodFailure(t *testing.T) {
	const prefix = "rolling out: 1 old replicas pending termination; 1 of 2 replicas unavailable: the rollout waits"
	tests := []struct {
		name    string
		deploy  *appsv1.Deployment
		pods    []runtime.Object
		want    string // contained in the reason
		without string // not contained in the reason
	}{
		{name: "an image that cannot be pulled", deploy: stuckRollout(),
			pods: []runtime.Object{pod("web-7d9f-abcde", "7d9f", running("sidecar", true),
				waiting("app", "ErrImagePull", "rpc error: failed to pull image \"ghcr.io/org/app:v2\": not found"))},
			want: prefix + ` for new pods to become available (if this lasts, check the new pods, for example for an image that cannot be pulled) (Available=True); ` +
				`new pod web-7d9f-abcde: container app is waiting: ErrImagePull: rpc error: failed to pull image "ghcr.io/org/app:v2": not found`},
		{name: "the condition from the ReplicaSet's creation names it too",
			deploy: func() *appsv1.Deployment {
				d := stuckRollout()
				d.Status.Conditions[1].Reason, d.Status.Conditions[1].Message = "NewReplicaSetCreated", `Created new replica set "web-7d9f"`
				return d
			}(),
			pods: []runtime.Object{pod("web-7d9f-abcde", "7d9f", waiting("app", "ImagePullBackOff", "Back-off pulling image"))},
			want: "; new pod web-7d9f-abcde: container app is waiting: ImagePullBackOff: Back-off pulling image"},
		{name: "a long multibyte message is cut on a rune boundary", deploy: stuckRollout(),
			pods: []runtime.Object{pod("web-7d9f-abcde", "7d9f", waiting("app", "CrashLoopBackOff", "x"+strings.Repeat("é", 150)))},
			want: "; new pod web-7d9f-abcde: container app is waiting: CrashLoopBackOff: x" + strings.Repeat("é", 99) + "..."},
		{name: "a crash loop", deploy: stuckRollout(),
			pods: []runtime.Object{pod("web-7d9f-abcde", "7d9f", waiting("app", "CrashLoopBackOff", "back-off 40s restarting failed container"))},
			want: "; new pod web-7d9f-abcde: container app is waiting: CrashLoopBackOff: back-off 40s restarting failed container"},
		{name: "a readiness probe that fails", deploy: stuckRollout(),
			pods: []runtime.Object{pod("web-7d9f-abcde", "7d9f", running("app", false))},
			want: "; new pod web-7d9f-abcde: container app is running but not ready (readiness probe)"},
		{name: "an unready pod of the old ReplicaSet does not count", deploy: stuckRollout(),
			pods: []runtime.Object{pod("web-5c4b-zzzzz", "5c4b", waiting("app", "CrashLoopBackOff", ""))},
			want: prefix, without: "new pod web"},
		{name: "no pods readable", deploy: stuckRollout(), want: prefix, without: "new pod web"},
		{name: "a finished rollout reads no pods",
			deploy: deployment("web", "ghcr.io/org/app:v2", nil),
			pods:   []runtime.Object{pod("web-7d9f-abcde", "7d9f", waiting("app", "ErrImagePull", ""))},
			want:   "2/2 replicas updated and available", without: "new pod web"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(tt.deploy).Build()
			dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{{Version: "v1", Resource: "pods"}: "PodList"}, tt.pods...)
			got, err := health.NewDeploymentAdapter(c, dyn).Check(context.Background(), health.CheckOptions{
				Resource:       health.ResourceConfig{Name: "web", Namespace: "prod"},
				ExpectedImages: []health.ImageExpectation{{Repository: "ghcr.io/org/app", Tag: "v2"}},
			})
			require.NoError(t, err)
			assert.True(t, utf8.ValidString(got.Reason), "the reason is valid UTF-8: %q", got.Reason)
			assert.Contains(t, got.Reason, tt.want)
			if tt.without != "" {
				assert.False(t, strings.Contains(got.Reason, tt.without), got.Reason)
			}
		})
	}
}
