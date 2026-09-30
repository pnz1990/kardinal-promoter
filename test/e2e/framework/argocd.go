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

// ArgoApp creates an auto-syncing Argo CD Application that deploys path of
// repo into destNS, and deletes it when the test ends.
func (e *Env) ArgoApp(t *testing.T, name string, repo gitserver.Repo, path, destNS string) {
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
				"server":    "https://kubernetes.default.svc",
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
