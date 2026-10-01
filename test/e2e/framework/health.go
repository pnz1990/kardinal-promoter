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

// SetArgoSyncRetry sets how often Argo CD retries a failed automated sync of
// the Application. Argo CD 3 retries 5 times by default, with a backoff from
// 5s, so a failing sync stays Running for minutes before it is Failed.
func (e *Env) SetArgoSyncRetry(t *testing.T, name string, limit int) {
	t.Helper()
	patch := fmt.Sprintf(`{"spec":{"syncPolicy":{"retry":{"limit":%d}}}}`, limit)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := e.Dynamic.Resource(ApplicationGVR).Namespace(ArgoCDNamespace).Patch(ctx, name,
		types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Fatalf("set sync retry limit %d on Argo CD Application %s: %v", limit, name, err)
	}
}

// SetArgoTargetRevision points the Argo CD Application at rev (a branch or a
// commit). Pinned to a commit, it keeps running that commit while its branch
// moves on.
func (e *Env) SetArgoTargetRevision(t *testing.T, name, rev string) {
	t.Helper()
	patch := fmt.Sprintf(`{"spec":{"source":{"targetRevision":%q}}}`, rev)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := e.Dynamic.Resource(ApplicationGVR).Namespace(ArgoCDNamespace).Patch(ctx, name,
		types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Fatalf("set targetRevision=%s on Argo CD Application %s: %v", rev, name, err)
	}
}

// SyncArgoApp starts a sync of the Argo CD Application's target revision, as
// `argocd app sync` does, with no retry. The Application must have no
// operation running.
func (e *Env) SyncArgoApp(t *testing.T, name string) {
	t.Helper()
	patch := `{"operation":{"initiatedBy":{"username":"kardinal-e2e"},"sync":{"syncStrategy":{"hook":{}}},"retry":{"limit":0}}}`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := e.Dynamic.Resource(ApplicationGVR).Namespace(ArgoCDNamespace).Patch(ctx, name,
		types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		t.Fatalf("sync Argo CD Application %s: %v", name, err)
	}
}

// WaitArgoOperation waits until the Application's last operation is in phase
// and returns the revision it ran on (status.operationState.syncResult.revision).
func (e *Env) WaitArgoOperation(t *testing.T, name, phase string, timeout time.Duration) string {
	t.Helper()
	var rev string
	Eventually(t, timeout, fmt.Sprintf("Argo CD Application %s operation %s", name, phase), func(ctx context.Context) (bool, string) {
		app, err := e.Dynamic.Resource(ApplicationGVR).Namespace(ArgoCDNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		got, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
		msg, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "message")
		rev, _, _ = unstructured.NestedString(app.Object, "status", "operationState", "syncResult", "revision")
		return got == phase && rev != "", fmt.Sprintf("phase=%s revision=%s: %s", got, rev, msg)
	})
	return rev
}

// CreateMarker creates a Service named name in ns, without endpoints, that a
// hook script can wait for: its DNS name resolves once it exists
// (fixtures.MarkerExists). It goes with the namespace.
func (e *Env) CreateMarker(t *testing.T, ns, name string) {
	t.Helper()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "marker", Port: 80}}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.Client.Create(ctx, svc); err != nil {
		t.Fatalf("create marker Service %s/%s: %v", ns, name, err)
	}
}

// ArgoField returns a string field of the Argo CD Application, "" when unset.
func (e *Env) ArgoField(t *testing.T, name string, fields ...string) string {
	t.Helper()
	v, _, _ := unstructured.NestedString(e.GetArgoApp(t, name).Object, fields...)
	return v
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
