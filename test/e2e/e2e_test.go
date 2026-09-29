//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Cluster e2e tests. Built only with `-tags e2e`, and they only talk to the
// kind context named in KARDINAL_E2E_CONTEXT (never the current context):
//
//	make e2e-setup
//	make test-e2e-kind
package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kardinal-promoter/kardinal-promoter/test/kindcontext"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// infraClient returns a dynamic client for the kind context named in
// KARDINAL_E2E_CONTEXT. It skips when the variable is unset and fails when it
// names anything other than a kind-* context.
func infraClient(t *testing.T) dynamic.Interface {
	t.Helper()
	kubeContext, ok, err := kindcontext.FromEnv(os.Getenv(kindcontext.EnvVar))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skipf("%s is not set — cluster e2e tests need an explicit kind context", kindcontext.EnvVar)
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		t.Fatalf("load kube context %s: %v", kubeContext, err)
	}
	c, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("create dynamic client for %s: %v", kubeContext, err)
	}
	return c
}

// TestInfrastructure verifies that the test cluster has both kro and
// kardinal-promoter installed and healthy. This test must pass before any
// journey test is meaningful.
func TestInfrastructure(t *testing.T) {
	client := infraClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Verify the kro Graph CRD is installed
	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}
	_, err := client.Resource(crdGVR).Get(ctx, "graphs.kro.run", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("kro Graph CRD not installed (graphs.kro.run): %v\n"+
			"Run: make e2e-setup to create a cluster with kro installed.", err)
	}
	t.Log("✅ kro Graph CRD installed: graphs.kro.run")

	_, err = client.Resource(crdGVR).Get(ctx, "graphrevisions.internal.kro.run", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("kro GraphRevision CRD not installed: %v", err)
	}
	t.Log("✅ kro GraphRevision CRD installed: graphrevisions.internal.kro.run")

	// Verify kardinal CRDs are installed
	kardinalCRDs := []string{
		"pipelines.kardinal.io",
		"bundles.kardinal.io",
		"policygates.kardinal.io",
		"promotionsteps.kardinal.io",
	}
	for _, crd := range kardinalCRDs {
		_, err := client.Resource(crdGVR).Get(ctx, crd, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("kardinal CRD not installed (%s): %v\nRun: make e2e-setup", crd, err)
		}
		t.Logf("✅ kardinal CRD installed: %s", crd)
	}

	// Verify the kro controller pod is running in kro-system
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	pods, err := client.Resource(podGVR).Namespace("kro-system").List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=kro",
	})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("kro controller pod not found in kro-system namespace: %v\n"+
			"Run: make install-kro", err)
	}
	t.Logf("✅ kro controller pod found in kro-system (%d pod(s))", len(pods.Items))
}
