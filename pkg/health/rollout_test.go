// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// wantKind is the expected classification of a HealthStatus.
type wantKind int

const (
	isHealthy wantKind = iota
	isProgressing
	isUnhealthy
	isTerminal
)

func kindOf(st health.HealthStatus) wantKind {
	switch {
	case st.Healthy:
		return isHealthy
	case st.Terminal:
		return isTerminal
	case st.Progressing:
		return isProgressing
	default:
		return isUnhealthy
	}
}

// deployment builds a Deployment whose rollout of image to generation 2 is
// complete; mutate breaks one part of it.
func deployment(name, image string, mutate func(d *appsv1.Deployment)) *appsv1.Deployment {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", Generation: 2,
			Labels: map[string]string{"app": "web"}},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(2)),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
				{Name: "app", Image: image},
				{Name: "sidecar", Image: "docker.io/envoyproxy/envoy:v1.30"},
			}}},
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2, Replicas: 2, UpdatedReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
				{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable"},
			},
		},
	}
	if mutate != nil {
		mutate(d)
	}
	return d
}

func setProgressing(d *appsv1.Deployment, status corev1.ConditionStatus, reason string) {
	d.Status.Conditions[1].Status = status
	d.Status.Conditions[1].Reason = reason
}

// TestDeploymentAdapter_RolloutAndImage proves C06-scm-health-07 and the
// Deployment half of C03-promotionstep-11: Available=True is not enough; the
// adapter must see the Bundle image and a finished rollout of the current
// generation, and a stalled rollout is terminal.
func TestDeploymentAdapter_RolloutAndImage(t *testing.T) {
	v2 := []health.ImageExpectation{{Repository: "ghcr.io/org/app", Tag: "v2"}}
	tests := []struct {
		name     string
		deploy   *appsv1.Deployment
		expected []health.ImageExpectation
		want     wantKind
		reason   string
	}{
		{name: "rolled out with the Bundle image", deploy: deployment("web", "ghcr.io/org/app:v2", nil),
			expected: v2, want: isHealthy, reason: "2/2 replicas updated and available"},
		{name: "old image still in the pod template", deploy: deployment("web", "ghcr.io/org/app:v1", nil),
			expected: v2, want: isProgressing, reason: "runs ghcr.io/org/app:v1, Bundle has ghcr.io/org/app:v2"},
		{name: "generation not observed yet",
			deploy: deployment("web", "ghcr.io/org/app:v2", func(d *appsv1.Deployment) { d.Status.ObservedGeneration = 1 }),
			want:   isProgressing, reason: "observe generation 2"},
		{name: "ProgressDeadlineExceeded is terminal",
			deploy: deployment("web", "ghcr.io/org/app:v2", func(d *appsv1.Deployment) {
				d.Status.UpdatedReplicas, d.Status.AvailableReplicas = 1, 1
				setProgressing(d, corev1.ConditionFalse, "ProgressDeadlineExceeded")
			}),
			want: isTerminal, reason: "ProgressDeadlineExceeded"},
		{name: "not every replica updated",
			deploy: deployment("web", "ghcr.io/org/app:v2", func(d *appsv1.Deployment) {
				d.Status.UpdatedReplicas = 1
				setProgressing(d, corev1.ConditionTrue, "ReplicaSetUpdated")
			}),
			want: isProgressing, reason: "1 of 2 replicas updated"},
		{name: "old replicas still running",
			deploy: deployment("web", "ghcr.io/org/app:v2", func(d *appsv1.Deployment) { d.Status.Replicas = 3 }),
			want:   isProgressing, reason: "1 old replicas pending termination (Available=True)"},
		{name: "the new pod of a one-replica rollout never becomes available",
			deploy: deployment("web", "ghcr.io/org/app:v2", func(d *appsv1.Deployment) {
				*d.Spec.Replicas = 1
				d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.ReadyReplicas = 2, 1, 1
				d.Status.AvailableReplicas, d.Status.UnavailableReplicas = 1, 1
				setProgressing(d, corev1.ConditionTrue, "ReplicaSetUpdated")
			}),
			want: isProgressing,
			reason: "rolling out: 1 old replicas pending termination; 1 of 2 replicas unavailable: the rollout waits " +
				"for new pods to become available (if this lasts, check the new pods, for example for an image " +
				"that cannot be pulled) (Available=True)"},
		{name: "the new pod of a two-replica rollout never becomes available",
			deploy: deployment("web", "ghcr.io/org/app:v2", func(d *appsv1.Deployment) {
				d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.ReadyReplicas = 3, 1, 2
				d.Status.AvailableReplicas, d.Status.UnavailableReplicas = 2, 1
				setProgressing(d, corev1.ConditionTrue, "ReplicaSetUpdated")
			}),
			want:   isProgressing,
			reason: "rolling out: 1 of 2 replicas updated; 1 of 3 replicas unavailable: the rollout waits for new pods"},
		{name: "updated replicas starting during the rollout",
			deploy: deployment("web", "ghcr.io/org/app:v2", func(d *appsv1.Deployment) {
				d.Status.AvailableReplicas = 1
				setProgressing(d, corev1.ConditionTrue, "ReplicaSetUpdated")
			}),
			want: isProgressing, reason: "1 of 2 updated replicas available"},
		{name: "replicas lost after the rollout finished",
			deploy: deployment("web", "ghcr.io/org/app:v2", func(d *appsv1.Deployment) {
				d.Status.AvailableReplicas = 0
				d.Status.Conditions[0].Status = corev1.ConditionFalse
			}),
			want: isUnhealthy, reason: "Available=False"},
		{name: "digest pinned image matches by digest",
			deploy:   deployment("web", "ghcr.io/org/app@sha256:bbb", nil),
			expected: []health.ImageExpectation{{Repository: "ghcr.io/org/app", Tag: "v2", Digest: "sha256:bbb"}},
			want:     isHealthy},
		{name: "digest expected but tag running",
			deploy:   deployment("web", "ghcr.io/org/app:v2", nil),
			expected: []health.ImageExpectation{{Repository: "ghcr.io/org/app", Digest: "sha256:bbb"}},
			want:     isProgressing},
		{name: "docker hub short names compare equal",
			deploy:   deployment("web", "nginx:1.29", nil),
			expected: []health.ImageExpectation{{Repository: "docker.io/library/nginx", Tag: "1.29"}},
			want:     isHealthy},
		{name: "workload runs none of the Bundle repositories",
			deploy:   deployment("web", "registry.local:5000/other:v9", nil),
			expected: v2, want: isHealthy, reason: "image not verified"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(tt.deploy).Build()
			got, err := health.NewDeploymentAdapter(c).Check(context.Background(), health.CheckOptions{
				Resource:       health.ResourceConfig{Name: "web", Namespace: "prod"},
				ExpectedImages: tt.expected,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
		})
	}
}

// TestDeploymentAdapter_LabelSelector proves C06-scm-health-09 / C01-graph-07:
// health.labelSelector checks every matching Deployment.
func TestDeploymentAdapter_LabelSelector(t *testing.T) {
	behind := deployment("web-b", "ghcr.io/org/app:v2", func(d *appsv1.Deployment) { d.Status.ObservedGeneration = 1 })
	tests := []struct {
		name    string
		objects []*appsv1.Deployment
		want    wantKind
		reason  string
	}{
		{name: "all matching Deployments rolled out",
			objects: []*appsv1.Deployment{deployment("web-a", "ghcr.io/org/app:v2", nil), deployment("web-b", "ghcr.io/org/app:v2", nil)},
			want:    isHealthy, reason: "2 Deployments matching"},
		{name: "one matching Deployment behind",
			objects: []*appsv1.Deployment{deployment("web-a", "ghcr.io/org/app:v2", nil), behind},
			want:    isProgressing, reason: "prod/web-b"},
		{name: "no Deployment matches", want: isUnhealthy, reason: "no Deployment in namespace prod matches"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(buildScheme(t))
			for _, d := range tt.objects {
				b = b.WithObjects(d)
			}
			// A Deployment with the right name but without the labels must not count.
			other := deployment("web", "ghcr.io/org/app:v1", func(d *appsv1.Deployment) { d.Labels = nil })
			got, err := health.NewDeploymentAdapter(b.WithObjects(other).Build()).Check(context.Background(), health.CheckOptions{
				Resource: health.ResourceConfig{Name: "web", Namespace: "prod",
					LabelSelector: map[string]string{"app": "web"}},
				ExpectedImages: []health.ImageExpectation{{Repository: "ghcr.io/org/app", Tag: "v2"}},
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
		})
	}
}

func argoApp(status map[string]interface{}) *unstructured.Unstructured {
	app := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"name": "web-prod", "namespace": "argocd"},
		"status":     status,
	}}
	return app
}

func synced(extra map[string]interface{}) map[string]interface{} {
	st := map[string]interface{}{
		"health":         map[string]interface{}{"status": "Healthy"},
		"sync":           map[string]interface{}{"status": "Synced", "revision": "0ld0000c0mmit11111111111111111111111111"},
		"operationState": map[string]interface{}{"phase": "Succeeded"},
	}
	for k, v := range extra {
		st[k] = v
	}
	return st
}

// TestArgoCDAdapter_Revision proves E2E-01 and the Argo CD half of
// C03-promotionstep-11: Healthy+Synced on the previous commit is not Verified.
func TestArgoCDAdapter_Revision(t *testing.T) {
	const pushed = "e7ddb9e5a1b2c3d4e5f60718293a4b5c6d7e8f90"
	tests := []struct {
		name     string
		status   map[string]interface{}
		revision string
		images   []health.ImageExpectation
		want     wantKind
		reason   string
	}{
		{name: "synced to the previous commit", status: synced(nil), revision: pushed,
			want: isProgressing, reason: "waiting for e7ddb9e5a1b2"},
		{name: "synced to the pushed commit",
			status:   synced(map[string]interface{}{"sync": map[string]interface{}{"status": "Synced", "revision": pushed}}),
			revision: pushed, want: isHealthy},
		{name: "abbreviated expected revision",
			status:   synced(map[string]interface{}{"sync": map[string]interface{}{"status": "Synced", "revision": pushed}}),
			revision: "e7ddb9e", want: isHealthy},
		{name: "multi-source revisions",
			status: synced(map[string]interface{}{"sync": map[string]interface{}{"status": "Synced",
				"revisions": []interface{}{"1.2.3", pushed}}}),
			revision: pushed, want: isHealthy},
		{name: "commit recorded in history, newer commit synced since",
			status: synced(map[string]interface{}{"history": []interface{}{
				map[string]interface{}{"revision": pushed},
			}}),
			revision: pushed, want: isHealthy},
		{name: "newer commit synced and the Application runs the Bundle image",
			status: synced(map[string]interface{}{"summary": map[string]interface{}{
				"images": []interface{}{"ghcr.io/org/app:v2"}}}),
			revision: pushed, images: []health.ImageExpectation{{Repository: "ghcr.io/org/app", Tag: "v2"}},
			want: isHealthy, reason: "runs the Bundle images"},
		{name: "previous commit synced with the old image",
			status: synced(map[string]interface{}{"summary": map[string]interface{}{
				"images": []interface{}{"ghcr.io/org/app:v1"}}}),
			revision: pushed, images: []health.ImageExpectation{{Repository: "ghcr.io/org/app", Tag: "v2"}},
			want: isProgressing, reason: "waiting for"},
		{name: "pushed commit synced but still progressing",
			status: synced(map[string]interface{}{
				"health": map[string]interface{}{"status": "Progressing"},
				"sync":   map[string]interface{}{"status": "Synced", "revision": pushed}}),
			revision: pushed, want: isProgressing},
		{name: "degraded is a failure", revision: pushed,
			status: synced(map[string]interface{}{"health": map[string]interface{}{"status": "Degraded"}}),
			want:   isUnhealthy},
		{name: "failed operation is a failure", revision: pushed,
			status: synced(map[string]interface{}{"operationState": map[string]interface{}{"phase": "Failed"}}),
			want:   isUnhealthy},
		{name: "no revision: images from the summary match",
			status: synced(map[string]interface{}{"summary": map[string]interface{}{
				"images": []interface{}{"ghcr.io/org/app:v2"}}}),
			images: []health.ImageExpectation{{Repository: "ghcr.io/org/app", Tag: "v2"}}, want: isHealthy},
		{name: "no revision: summary still has the old image",
			status: synced(map[string]interface{}{"summary": map[string]interface{}{
				"images": []interface{}{"ghcr.io/org/app:v1"}}}),
			images: []health.ImageExpectation{{Repository: "ghcr.io/org/app", Tag: "v2"}}, want: isProgressing},
		{name: "nothing to compare", status: synced(nil), want: isHealthy, reason: "(revision not verified)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dyn := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), argoApp(tt.status))
			got, err := health.NewArgoCDAdapter(dyn).Check(context.Background(), health.CheckOptions{
				ArgoCD:           health.ArgoCDConfig{Name: "web-prod", Namespace: "argocd"},
				ExpectedRevision: tt.revision,
				ExpectedImages:   tt.images,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
		})
	}
}

// TestFluxAdapter_Revision: Flux is Verified only once lastAppliedRevision is the promoted commit.
func TestFluxAdapter_Revision(t *testing.T) {
	const pushed = "034ce92a1b2c3d4e5f60718293a4b5c6d7e8f901"
	ks := func(ready, applied string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
			"kind":       "Kustomization",
			"metadata":   map[string]interface{}{"name": "web-prod", "namespace": "flux-system", "generation": int64(3)},
			"status": map[string]interface{}{
				"observedGeneration":  int64(3),
				"lastAppliedRevision": applied,
				"conditions":          []interface{}{map[string]interface{}{"type": "Ready", "status": ready}},
			},
		}}
	}
	tests := []struct {
		name   string
		obj    *unstructured.Unstructured
		want   wantKind
		reason string
	}{
		{name: "previous commit applied", obj: ks("True", "main@sha1:d7d4d8a000000000000000000000000000000000"),
			want: isProgressing, reason: "waiting for 034ce92a1b2c"},
		{name: "pushed commit applied", obj: ks("True", "main@sha1:"+pushed), want: isHealthy,
			reason: "Ready=True, generation=3 matches, lastAppliedRevision=034ce92a1b2c"},
		{name: "legacy revision format", obj: ks("True", "main/"+pushed), want: isHealthy,
			reason: "lastAppliedRevision=034ce92a1b2c"},
		{name: "OCI source cannot be compared", obj: ks("True", "latest@sha256:abc"), want: isHealthy,
			reason: "revision not verified"},
		{name: "Ready=Unknown is progressing", obj: ks("Unknown", "main@sha1:"+pushed), want: isProgressing},
		{name: "Ready=False is a failure", obj: ks("False", "main@sha1:"+pushed), want: isUnhealthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dyn := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), tt.obj)
			got, err := health.NewFluxAdapter(dyn).Check(context.Background(), health.CheckOptions{
				Flux: health.FluxConfig{Name: "web-prod", Namespace: "flux-system"}, ExpectedRevision: pushed,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
			assert.Contains(t, got.Reason, tt.reason)
		})
	}
}

// TestProgressiveDeliveryPhases proves C06-scm-health-08: in-flight canary and
// rollout phases are progressing (not failures that feed auto-rollback) and a
// failed canary is terminal.
func TestProgressiveDeliveryPhases(t *testing.T) {
	obj := func(apiVersion, kind, phase string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": apiVersion, "kind": kind,
			"metadata": map[string]interface{}{"name": "web", "namespace": "prod", "generation": int64(1)},
			"status":   map[string]interface{}{"phase": phase, "observedGeneration": "1"},
		}}
	}
	tests := []struct {
		name  string
		obj   *unstructured.Unstructured
		check func(dyn *dynfake.FakeDynamicClient) (health.HealthStatus, error)
		want  wantKind
	}{}
	flagger := func(dyn *dynfake.FakeDynamicClient) (health.HealthStatus, error) {
		return health.NewFlaggerAdapter(dyn).Check(context.Background(),
			health.CheckOptions{Flagger: health.FlaggerConfig{Name: "web", Namespace: "prod"}})
	}
	rollouts := func(dyn *dynfake.FakeDynamicClient) (health.HealthStatus, error) {
		return health.NewArgoRolloutsAdapter(dyn).Check(context.Background(),
			health.CheckOptions{ArgoRollouts: health.ArgoRolloutsConfig{Name: "web", Namespace: "prod"}})
	}
	for phase, want := range map[string]wantKind{
		"Succeeded": isHealthy, "Failed": isTerminal, "Progressing": isProgressing,
		"Initialized": isProgressing, "WaitingPromotion": isProgressing, "Finalising": isProgressing,
	} {
		tests = append(tests, struct {
			name  string
			obj   *unstructured.Unstructured
			check func(dyn *dynfake.FakeDynamicClient) (health.HealthStatus, error)
			want  wantKind
		}{name: "flagger " + phase, obj: obj("flagger.app/v1beta1", "Canary", phase), check: flagger, want: want})
	}
	for phase, want := range map[string]wantKind{
		"Healthy": isHealthy, "Degraded": isUnhealthy, "Progressing": isProgressing, "Paused": isProgressing,
	} {
		tests = append(tests, struct {
			name  string
			obj   *unstructured.Unstructured
			check func(dyn *dynfake.FakeDynamicClient) (health.HealthStatus, error)
			want  wantKind
		}{name: "rollout " + phase, obj: obj("argoproj.io/v1alpha1", "Rollout", phase), check: rollouts, want: want})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.check(dynfake.NewSimpleDynamicClient(runtime.NewScheme(), tt.obj))
			require.NoError(t, err)
			assert.Equal(t, tt.want, kindOf(got), got.Reason)
		})
	}
}

func TestParseImageAndSameRevision(t *testing.T) {
	images := []struct {
		ref  string
		want health.ImageExpectation
	}{
		{"nginx", health.ImageExpectation{Repository: "nginx"}},
		{"ghcr.io/org/app:v2", health.ImageExpectation{Repository: "ghcr.io/org/app", Tag: "v2"}},
		{"registry.local:5000/app", health.ImageExpectation{Repository: "registry.local:5000/app"}},
		{"registry.local:5000/app:v1@sha256:abc", health.ImageExpectation{Repository: "registry.local:5000/app", Tag: "v1", Digest: "sha256:abc"}},
	}
	for _, tt := range images {
		assert.Equal(t, tt.want, health.ParseImage(tt.ref), tt.ref)
	}
	revisions := []struct {
		a, b string
		want bool
	}{
		{"e7ddb9e5a1b2c3", "e7ddb9e", true},
		{"E7DDB9E", "e7ddb9e5a1", true},
		{"e7ddb9e", "d7d4d8a", false},
		{"e7d", "e7ddb9e", false},
		{"", "", false},
		{"1.2.3", "1.2.3", true},
	}
	for _, tt := range revisions {
		assert.Equal(t, tt.want, health.SameRevision(tt.a, tt.b), "%s vs %s", tt.a, tt.b)
	}
}
