// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// ArgoCDNamespace is where hack/e2e/components/argocd.sh installs Argo CD.
const ArgoCDNamespace = "argocd"

// ApplicationGVR is Argo CD's Application.
var ApplicationGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// InClusterServer is the destination.server of an Application that deploys
// into the cluster Argo CD runs in.
const InClusterServer = "https://kubernetes.default.svc"

// ArgoApp creates an auto-syncing Argo CD Application that deploys path of
// repo into destNS, and deletes it when the test ends.
func (e *Env) ArgoApp(t *testing.T, name string, repo gitserver.Repo, path, destNS string) {
	t.Helper()
	e.ArgoAppIn(t, InClusterServer, name, repo, path, destNS)
}

// ArgoAppIn is ArgoApp deploying into the cluster whose API server is server,
// one registered with Argo CD (the multi-cluster suite's spoke).
func (e *Env) ArgoAppIn(t *testing.T, server, name string, repo gitserver.Repo, path, destNS string) {
	t.Helper()
	app := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": ArgoCDNamespace,
			"labels":    map[string]interface{}{"kardinal.io/e2e": "true"},
		},
		"spec": map[string]interface{}{
			"project": "default",
			"source": map[string]interface{}{
				"repoURL":        repo.CloneURL,
				"targetRevision": repo.Branch,
				"path":           path,
			},
			"destination": map[string]interface{}{
				"server":    server,
				"namespace": destNS,
			},
			"syncPolicy": map[string]interface{}{
				"automated": map[string]interface{}{"prune": true, "selfHeal": true},
			},
		},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := e.Dynamic.Resource(ApplicationGVR).Namespace(ArgoCDNamespace).Create(ctx, app, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create Argo CD Application %s: %v", name, err)
	}
	t.Cleanup(func() {
		if os.Getenv(EnvKeep) == "1" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := e.Dynamic.Resource(ApplicationGVR).Namespace(ArgoCDNamespace).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete Argo CD Application %s: %v", name, err)
		}
	})
}

// WaitArgoApp waits until the Application is Synced and Healthy.
func (e *Env) WaitArgoApp(t *testing.T, name string, timeout time.Duration) {
	t.Helper()
	Eventually(t, timeout, "Argo CD Application "+name+" Synced/Healthy", func(ctx context.Context) (bool, string) {
		app, err := e.Dynamic.Resource(ApplicationGVR).Namespace(ArgoCDNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		sync, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
		health, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
		return sync == "Synced" && health == "Healthy", fmt.Sprintf("%s/%s", sync, health)
	})
}

// ArgoAppStatus is what Argo CD reports for the Application: its health
// (status.health.status), sync status and synced revision.
type ArgoAppStatus struct {
	Health, Sync, Revision string
}

func (s ArgoAppStatus) String() string {
	return fmt.Sprintf("health=%s sync=%s revision=%s", s.Health, s.Sync, s.Revision)
}

// ArgoAppState reads the status of the Application name.
func (e *Env) ArgoAppState(ctx context.Context, name string) (ArgoAppStatus, error) {
	app, err := e.Dynamic.Resource(ApplicationGVR).Namespace(ArgoCDNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ArgoAppStatus{}, err
	}
	var s ArgoAppStatus
	s.Health, _, _ = unstructured.NestedString(app.Object, "status", "health", "status")
	s.Sync, _, _ = unstructured.NestedString(app.Object, "status", "sync", "status")
	s.Revision, _, _ = unstructured.NestedString(app.Object, "status", "sync", "revision")
	return s, nil
}

// ApplicationSetGVR is Argo CD's ApplicationSet.
var ApplicationSetGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}

// ApplicationSet creates the ApplicationSet set in ArgoCDNamespace. When the
// test ends it deletes it and waits until the Applications it generated are
// gone, so they no longer sync (or recreate namespaces) once the test's
// namespaces are deleted.
func (e *Env) ApplicationSet(t *testing.T, set *unstructured.Unstructured) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	set.SetNamespace(ArgoCDNamespace)
	res := e.Dynamic.Resource(ApplicationSetGVR).Namespace(ArgoCDNamespace)
	if _, err := res.Create(ctx, set, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create ApplicationSet %s: %v", set.GetName(), err)
	}
	t.Cleanup(func() {
		if os.Getenv(EnvKeep) == "1" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Foreground: the ApplicationSet is gone only once its Applications are.
		fg := metav1.DeletePropagationForeground
		err := res.Delete(ctx, set.GetName(), metav1.DeleteOptions{PropagationPolicy: &fg})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete ApplicationSet %s: %v", set.GetName(), err)
			return
		}
		Eventually(t, 2*time.Minute, "ApplicationSet "+set.GetName()+" and its Applications deleted", func(ctx context.Context) (bool, string) {
			_, err := res.Get(ctx, set.GetName(), metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, ""
			}
			return false, fmt.Sprintf("still there (err %v)", err)
		})
	})
}
