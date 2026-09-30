// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
)

var bundleCreated = regexp.MustCompile(`Bundle (\S+) created`)

// CreateBundle runs `kardinal create bundle pipeline args...` in ns and
// returns the new Bundle's name.
func (e *Env) CreateBundle(t *testing.T, ns, pipeline string, args ...string) string {
	t.Helper()
	out := e.MustKardinal(t, ns, append([]string{"create", "bundle", pipeline}, args...)...)
	m := bundleCreated.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("create bundle: no bundle name in output:\n%s", out)
	}
	return m[1]
}

// WaitDeploymentImage waits until the Deployment's first container runs
// image and every replica is updated and available: the change reached the
// cluster, not just git.
func (e *Env) WaitDeploymentImage(t *testing.T, ns, name, image string, timeout time.Duration) {
	t.Helper()
	Eventually(t, timeout, fmt.Sprintf("Deployment %s/%s rolled out %s", ns, name, image), func(ctx context.Context) (bool, string) {
		var d appsv1.Deployment
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &d); err != nil {
			return false, err.Error()
		}
		got := d.Spec.Template.Spec.Containers[0].Image
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		st := d.Status
		done := got == image && d.Generation == st.ObservedGeneration &&
			st.UpdatedReplicas == want && st.AvailableReplicas == want && st.Replicas == want
		return done, fmt.Sprintf("image %s, updated %d, available %d, total %d", got, st.UpdatedReplicas, st.AvailableReplicas, st.Replicas)
	})
}

// DeploymentImage returns the Deployment's first container image.
func (e *Env) DeploymentImage(t *testing.T, ns, name string) string {
	t.Helper()
	var d appsv1.Deployment
	if err := e.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &d); err != nil {
		t.Fatalf("get Deployment %s/%s: %v", ns, name, err)
	}
	return d.Spec.Template.Spec.Containers[0].Image
}
