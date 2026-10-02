// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// HoldPodDeletion waits until a pod in ns runs image, then makes the API
// server deny every delete of a pod in ns that runs image, with a
// ValidatingAdmissionPolicy, and waits until it does. The pods stay, not
// terminating, after their ReplicaSet scales down, so the Deployment
// controller does not see them go. lift deletes the policy and the pods it
// held, as the ReplicaSet controller would once allowed, and waits until
// they are gone; the policy is also deleted when the test ends.
//
// The wait probes with dry-run deletes, which the policy counts as denials.
func (e *Env) HoldPodDeletion(t *testing.T, ns, image string, timeout time.Duration) (lift func()) {
	t.Helper()
	held := func(ctx context.Context) ([]corev1.Pod, error) {
		var pods corev1.PodList
		if err := e.Client.List(ctx, &pods, client.InNamespace(ns)); err != nil {
			return nil, err
		}
		var out []corev1.Pod
		for _, p := range pods.Items {
			for _, c := range p.Spec.Containers {
				if c.Image == image {
					out = append(out, p)
					break
				}
			}
		}
		return out, nil
	}
	var probe string
	Eventually(t, timeout, fmt.Sprintf("a pod in %s to run %s", ns, image), func(ctx context.Context) (bool, string) {
		pods, err := held(ctx)
		if err != nil {
			return false, err.Error()
		}
		if len(pods) == 0 {
			return false, "no pod runs it"
		}
		probe = pods[0].Name
		return true, ""
	})
	policy := "e2e-hold-pods-" + ns
	remove := e.denyAdmission(t, policy, admissionv1.RuleWithOperations{
		Operations: []admissionv1.OperationType{admissionv1.Delete},
		Rule:       admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}},
	}, fmt.Sprintf("request.namespace == %q && oldObject.spec.containers.exists(c, c.image == %q)", ns, image),
		fmt.Sprintf("e2e: deletes of pods in %s that run %s are denied", ns, image))
	dryRunDelete := func(ctx context.Context) error {
		return e.Kube.CoreV1().Pods(ns).Delete(ctx, probe, metav1.DeleteOptions{DryRun: []string{metav1.DryRunAll}})
	}
	Eventually(t, time.Minute, "the API server to deny deletes of pods in "+ns+" that run "+image, func(ctx context.Context) (bool, string) {
		err := dryRunDelete(ctx)
		return err != nil && strings.Contains(err.Error(), policy), fmt.Sprintf("dry-run delete of pod %s: %v", probe, err)
	})
	return func() {
		t.Helper()
		if err := remove(); err != nil {
			t.Fatalf("delete admission policy %s: %v", policy, err)
		}
		Eventually(t, time.Minute, "the API server to allow deletes of pods in "+ns+" that run "+image, func(ctx context.Context) (bool, string) {
			err := dryRunDelete(ctx)
			return err == nil || apierrors.IsNotFound(err), fmt.Sprintf("dry-run delete of pod %s: %v", probe, err)
		})
		Eventually(t, time.Minute, fmt.Sprintf("the pods in %s that run %s to be gone", ns, image), func(ctx context.Context) (bool, string) {
			pods, err := held(ctx)
			if err != nil {
				return false, err.Error()
			}
			for _, p := range pods {
				if err := e.Kube.CoreV1().Pods(ns).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
					return false, fmt.Sprintf("delete pod %s: %v", p.Name, err)
				}
			}
			return len(pods) == 0, fmt.Sprintf("%d pods left", len(pods))
		})
	}
}
