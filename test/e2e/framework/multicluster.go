// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// Environment variables hack/e2e/components/spoke.sh sets for the
// multi-cluster suite.
const (
	// EnvSpokeContext is the kube context of the spoke, the suite's second
	// kind cluster. Argo CD and Flux in the hub (the test cluster) manage it;
	// kardinal has no access to it.
	EnvSpokeContext = "KARDINAL_E2E_SPOKE_CONTEXT"
	// EnvSpokeServer is the spoke's API server URL as the hub reaches it.
	EnvSpokeServer = "KARDINAL_E2E_SPOKE_SERVER"
)

// SpokeKubeconfigSecret is the Secret in FluxNamespace (the hub's Flux,
// hack/e2e/components/flux.sh) holding a kubeconfig for the spoke under key
// SpokeKubeconfigKey: what a hub Kustomization's spec.kubeConfig.secretRef
// names. hack/e2e/components/spoke.sh creates it.
const (
	SpokeKubeconfigSecret = "spoke-kubeconfig"
	SpokeKubeconfigKey    = "value"
)

// Spoke is the multi-cluster suite's workload cluster, for the test's own
// checks of what runs there. Server is its API server URL as the hub's Argo
// CD and Flux reach it.
type Spoke struct {
	*Env
	Server string
}

// Spoke connects to the spoke in KARDINAL_E2E_SPOKE_CONTEXT. It fails the
// test when the variable is unset, names a non-kind context, or names the
// hub.
func (e *Env) Spoke(t *testing.T) *Spoke {
	t.Helper()
	kubeContext := strings.TrimSpace(os.Getenv(EnvSpokeContext))
	server := os.Getenv(EnvSpokeServer)
	switch {
	case kubeContext == "" || server == "":
		t.Fatalf("%s or %s is not set; run hack/e2e/up.sh multi-cluster", EnvSpokeContext, EnvSpokeServer)
	case !strings.HasPrefix(kubeContext, "kind-"):
		t.Fatalf("%s=%q: refusing to use a non-kind context", EnvSpokeContext, kubeContext)
	case kubeContext == e.Context:
		t.Fatalf("%s is the test cluster %s, not a second cluster", EnvSpokeContext, e.Context)
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		t.Fatalf("load kube context %s: %v", kubeContext, err)
	}
	cfg.QPS, cfg.Burst = 50, 100
	c, err := client.New(cfg, client.Options{Scheme: Scheme()})
	if err != nil {
		t.Fatalf("spoke controller-runtime client: %v", err)
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("spoke kubernetes client: %v", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("spoke dynamic client: %v", err)
	}
	return &Spoke{
		Env:    &Env{Context: kubeContext, Config: cfg, Client: c, Kube: kube, Dynamic: dyn, Git: e.Git, cli: e.cli},
		Server: server,
	}
}

// Namespace creates the namespace name in the spoke (normally the test
// namespace's name, which the fixtures' kustomizations set) and deletes it
// when the test ends. On failure it first writes the spoke's diagnostics to
// $KARDINAL_E2E_ARTIFACTS/<name>/spoke/. KARDINAL_E2E_KEEP=1 keeps it.
//
// Call it before creating the Applications or Kustomizations that deploy
// into it: cleanups run last-in first-out, so they are deleted first and
// cannot recreate it.
func (s *Spoke) Namespace(t *testing.T, name string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{"kardinal.io/e2e": "true"},
	}}
	if err := s.Client.Create(context.Background(), ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s in the spoke: %v", name, err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			s.diagnose(t, name, filepath.Join(artifactsDir(), name, "spoke"))
		}
		if os.Getenv(EnvKeep) == "1" {
			t.Logf("keeping namespace %s in the spoke (%s=1)", name, EnvKeep)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.Client.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete namespace %s in the spoke: %v", name, err)
		}
	})
}

// RemoteKustomization creates, in the hub's FluxNamespace, a GitRepository
// and a Kustomization, both named name, that apply path of repo to the spoke:
// the Kustomization's spec.kubeConfig.secretRef is SpokeKubeconfigSecret.
// The Kustomization sets spec.wait, so it is Ready only once the applied
// workloads are, and gives up after timeout. A failed apply or wait is
// retried after 5m, so a failure stays visible for the rest of a test. Both
// objects are deleted when the test ends (Flux then prunes what the
// Kustomization applied); when the test failed, their status is logged
// first, since Diagnose dumps only the test namespace.
func (e *Env) RemoteKustomization(t *testing.T, name string, repo gitserver.Repo, path string, timeout time.Duration) {
	t.Helper()
	e.FluxGitRepository(t, FluxNamespace, name, map[string]interface{}{
		"url":      repo.CloneURL,
		"ref":      map[string]interface{}{"branch": repo.Branch},
		"interval": "10s",
		"timeout":  "30s",
	})
	e.FluxKustomization(t, FluxNamespace, name, map[string]interface{}{
		"sourceRef":     map[string]interface{}{"kind": "GitRepository", "name": name},
		"path":          "./" + path,
		"prune":         true,
		"wait":          true,
		"timeout":       timeout.String(),
		"interval":      "10m",
		"retryInterval": "5m",
		"kubeConfig": map[string]interface{}{"secretRef": map[string]interface{}{
			"name": SpokeKubeconfigSecret, "key": SpokeKubeconfigKey,
		}},
	})
	// Registered last, so it runs before the deletes.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, gvr := range []schema.GroupVersionResource{GitRepositoryGVR, KustomizationGVR} {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			obj, err := e.FluxObject(ctx, gvr, FluxNamespace, name)
			cancel()
			if err != nil {
				t.Logf("get %s %s/%s: %v", gvr.Resource, FluxNamespace, name, err)
				continue
			}
			status, _, _ := unstructured.NestedMap(obj.Object, "status")
			t.Logf("%s %s/%s status: %v", gvr.Resource, FluxNamespace, name, status)
		}
	})
}
