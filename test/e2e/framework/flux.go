// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// FluxNamespace is where hack/e2e/components/flux.sh installs Flux.
const FluxNamespace = "flux-system"

// Flux's GitRepository and Kustomization.
var (
	GitRepositoryGVR = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
	KustomizationGVR = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
)

// FluxSource creates a GitRepository ns/name that fetches repo's branch every
// interval, and deletes it when the test ends. The e2e git server's repos are
// public, so it needs no credentials.
func (e *Env) FluxSource(t *testing.T, ns, name string, repo gitserver.Repo, interval time.Duration) {
	t.Helper()
	e.FluxGitRepository(t, ns, name, map[string]interface{}{
		"url":      repo.CloneURL,
		"ref":      map[string]interface{}{"branch": repo.Branch},
		"interval": interval.String(),
		"timeout":  "30s",
	})
}

// FluxGitRepository creates a GitRepository ns/name with spec, and deletes it
// when the test ends.
func (e *Env) FluxGitRepository(t *testing.T, ns, name string, spec map[string]interface{}) {
	t.Helper()
	e.createFlux(t, GitRepositoryGVR, "GitRepository", ns, name, spec)
}

// FluxKustomization creates a Kustomization ns/name with spec, and deletes it
// when the test ends.
func (e *Env) FluxKustomization(t *testing.T, ns, name string, spec map[string]interface{}) {
	t.Helper()
	e.createFlux(t, KustomizationGVR, "Kustomization", ns, name, spec)
}

func (e *Env) createFlux(t *testing.T, gvr schema.GroupVersionResource, kind, ns, name string, spec map[string]interface{}) {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": gvr.GroupVersion().String(),
		"kind":       kind,
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": ns,
			"labels":    map[string]interface{}{"kardinal.io/e2e": "true"},
		},
		"spec": spec,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := e.Dynamic.Resource(gvr).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create %s %s/%s: %v", kind, ns, name, err)
	}
	t.Cleanup(func() {
		if os.Getenv(EnvKeep) == "1" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := e.Dynamic.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete %s %s/%s: %v", kind, ns, name, err)
		}
	})
}

// FluxObject gets a Flux object.
func (e *Env) FluxObject(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error) {
	return e.Dynamic.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
}

// FluxReady returns the Ready condition's status, reason and message, and
// whether status.observedGeneration matches metadata.generation.
func FluxReady(obj *unstructured.Unstructured) (status, reason, message string, current bool) {
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok || m["type"] != "Ready" {
			continue
		}
		status, _ = m["status"].(string)
		reason, _ = m["reason"].(string)
		message, _ = m["message"].(string)
	}
	observed, _, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration")
	return status, reason, message, observed == obj.GetGeneration()
}

// WaitFluxReady waits until the Flux object is Ready=True for its current
// generation.
func (e *Env) WaitFluxReady(t *testing.T, gvr schema.GroupVersionResource, ns, name string, timeout time.Duration) *unstructured.Unstructured {
	t.Helper()
	var got *unstructured.Unstructured
	Eventually(t, timeout, fmt.Sprintf("%s %s/%s Ready", gvr.Resource, ns, name), func(ctx context.Context) (bool, string) {
		obj, err := e.FluxObject(ctx, gvr, ns, name)
		if err != nil {
			return false, err.Error()
		}
		got = obj
		status, reason, msg, current := FluxReady(obj)
		return status == "True" && current, fmt.Sprintf("Ready=%s %s: %s (current generation: %t)", status, reason, msg, current)
	})
	return got
}

// PatchFlux merge-patches a Flux object, for example to suspend it.
func (e *Env) PatchFlux(t *testing.T, gvr schema.GroupVersionResource, ns, name string, patch map[string]interface{}) {
	t.Helper()
	body, err := json.Marshal(patch)
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := e.Dynamic.Resource(gvr).Namespace(ns).Patch(ctx, name, types.MergePatchType, body, metav1.PatchOptions{}); err != nil {
		t.Fatalf("patch %s %s/%s: %v", gvr.Resource, ns, name, err)
	}
}

// SuspendFlux sets spec.suspend on a Flux object.
func (e *Env) SuspendFlux(t *testing.T, gvr schema.GroupVersionResource, ns, name string, suspend bool) {
	t.Helper()
	e.PatchFlux(t, gvr, ns, name, map[string]interface{}{"spec": map[string]interface{}{"suspend": suspend}})
}

// ReconcileFlux asks Flux to reconcile the object now, as `flux reconcile`
// does.
func (e *Env) ReconcileFlux(t *testing.T, gvr schema.GroupVersionResource, ns, name string) {
	t.Helper()
	e.PatchFlux(t, gvr, ns, name, map[string]interface{}{"metadata": map[string]interface{}{
		"annotations": map[string]interface{}{"reconcile.fluxcd.io/requestedAt": time.Now().UTC().Format(time.RFC3339Nano)},
	}})
}

// KeepFluxReconciling asks Flux to reconcile the object again each time it
// handled the previous request, as `flux reconcile` in a loop or a busy
// webhook receiver does, until stop is called or the test ends. Flux marks
// the object Ready=Unknown during each reconcile, so it is reconciling most
// of the time. stop returns how many requests Flux handled.
func (e *Env) KeepFluxReconciling(t *testing.T, gvr schema.GroupVersionResource, ns, name string) (stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	handled := 0
	var lastErr error
	res := e.Dynamic.Resource(gvr).Namespace(ns)
	wait := func() {
		select {
		case <-ctx.Done():
		case <-time.After(Poll):
		}
	}
	go func() {
		defer close(done)
		token := ""
		request := func() {
			for ctx.Err() == nil {
				token = time.Now().UTC().Format(time.RFC3339Nano)
				body := fmt.Sprintf(`{"metadata":{"annotations":{"reconcile.fluxcd.io/requestedAt":%q}}}`, token)
				_, err := res.Patch(ctx, name, types.MergePatchType, []byte(body), metav1.PatchOptions{})
				if err == nil || ctx.Err() != nil {
					return
				}
				lastErr = err
				wait()
			}
		}
		request()
		for ctx.Err() == nil {
			// A watch without a resourceVersion starts with the object as it
			// is, so a request handled while no watch ran is not missed.
			w, err := res.Watch(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + name})
			if err != nil {
				if ctx.Err() == nil {
					lastErr = err
				}
				wait()
				continue
			}
			for ev := range w.ResultChan() {
				obj, ok := ev.Object.(*unstructured.Unstructured)
				if !ok {
					continue
				}
				if got, _, _ := unstructured.NestedString(obj.Object, "status", "lastHandledReconcileAt"); got == token {
					handled++
					request()
				}
			}
			w.Stop()
		}
	}()
	var once sync.Once
	stop = func() int {
		once.Do(func() {
			cancel()
			<-done
			if lastErr != nil {
				t.Logf("asking Flux to reconcile %s %s/%s: last error: %v", gvr.Resource, ns, name, lastErr)
			}
		})
		return handled
	}
	t.Cleanup(func() { stop() })
	return stop
}

// FluxAppliedRevision is a Kustomization's status.lastAppliedRevision, for
// example "main@sha1:<commit>".
func FluxAppliedRevision(obj *unstructured.Unstructured) string {
	rev, _, _ := unstructured.NestedString(obj.Object, "status", "lastAppliedRevision")
	return rev
}

// FluxArtifactRevision is a GitRepository's status.artifact.revision.
func FluxArtifactRevision(obj *unstructured.Unstructured) string {
	rev, _, _ := unstructured.NestedString(obj.Object, "status", "artifact", "revision")
	return rev
}
