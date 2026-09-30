// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// WaitStep waits until match accepts the Bundle's PromotionStep for env and
// returns the step. Unlike WaitStepState it does not fail on other terminal
// states, so it can wait for RollingBack, a message or a counter.
func (e *Env) WaitStep(t *testing.T, ns, pipeline, bundle, env string, timeout time.Duration, what string,
	match func(*v1alpha1.PromotionStep) bool) *v1alpha1.PromotionStep {
	t.Helper()
	var got *v1alpha1.PromotionStep
	Eventually(t, timeout, fmt.Sprintf("step %s/%s/%s: %s", pipeline, bundle, env, what), func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, ns, pipeline, bundle, env)
		if err != nil {
			return false, err.Error()
		}
		if !ok {
			return false, "no PromotionStep yet"
		}
		got = ps
		return match(ps), DescribeStep(ps)
	})
	return got
}

// MustStep returns the Bundle's PromotionStep for env, failing the test when
// it does not exist.
func (e *Env) MustStep(t *testing.T, ns, pipeline, bundle, env string) *v1alpha1.PromotionStep {
	t.Helper()
	ps, ok, err := e.Step(context.Background(), ns, pipeline, bundle, env)
	if err != nil || !ok {
		t.Fatalf("PromotionStep %s/%s/%s: ok=%v err=%v", pipeline, bundle, env, ok, err)
	}
	return ps
}

// DescribeStep is a one-line summary of a step for failure messages.
func DescribeStep(ps *v1alpha1.PromotionStep) string {
	s := ps.Status
	return fmt.Sprintf("state=%q failures=%d bakeResets=%d message=%q", s.State, s.ConsecutiveHealthFailures, s.BakeResets, s.Message)
}

// SetArgoAutoSync turns automated sync of the Argo CD Application on or off.
// Off, the Application keeps running what it last synced while git moves on.
func (e *Env) SetArgoAutoSync(t *testing.T, name string, on bool) {
	t.Helper()
	patch := `{"spec":{"syncPolicy":null}}`
	if on {
		patch = `{"spec":{"syncPolicy":{"automated":{"prune":true,"selfHeal":true}}}}`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := e.Dynamic.Resource(ApplicationGVR).Namespace(ArgoCDNamespace).Patch(ctx, name,
		types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Fatalf("set auto-sync=%v on Argo CD Application %s: %v", on, name, err)
	}
}

// GetArgoApp returns the Argo CD Application.
func (e *Env) GetArgoApp(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	app, err := e.Dynamic.Resource(ApplicationGVR).Namespace(ArgoCDNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Argo CD Application %s: %v", name, err)
	}
	return app
}

// SetReadyz flips podinfo's readiness on every Running pod the selector
// matches, through the API server's pod proxy (POST /readyz/enable or
// /readyz/disable). A disabled pod fails its readiness probe, so its
// Deployment loses availability without a rollout.
func (e *Env) SetReadyz(t *testing.T, ns string, selector map[string]string, ready bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var pods corev1.PodList
	if err := e.Client.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels(selector)); err != nil {
		t.Fatalf("list pods %v in %s: %v", selector, ns, err)
	}
	suffix := "readyz/disable"
	if ready {
		suffix = "readyz/enable"
	}
	n := 0
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
			continue
		}
		if err := e.Kube.CoreV1().RESTClient().Post().Namespace(ns).Resource("pods").
			Name(p.Name + ":9898").SubResource("proxy").Suffix(suffix).Do(ctx).Error(); err != nil {
			t.Fatalf("POST %s to pod %s/%s: %v", suffix, ns, p.Name, err)
		}
		n++
	}
	if n == 0 {
		t.Fatalf("no running pod matches %v in %s", selector, ns)
	}
}
