// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package e2e contains end-to-end journey tests for kardinal-promoter.
//
// Each test function corresponds to one journey in docs/aide/definition-of-done.md.
// The project is complete when all five journey tests pass against a real kind cluster.
//
// Prerequisites: a kind cluster with krocodile and kardinal-promoter installed.
// Use make kind-up to create the cluster (installs krocodile automatically).
//
// Run individual journeys:
//
//	make test-e2e-journey-1
//	make test-e2e-journey-2
//	...
//
// Run all journeys (creates and destroys kind cluster):
//
//	make test-e2e
package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// infraClient returns a dynamic client for the current kubeconfig context.
// Tests call t.Skip if the cluster is unreachable.
func infraClient(t *testing.T) dynamic.Interface {
	t.Helper()
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = clientcmd.RecommendedHomeFile
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Skipf("no cluster available (KUBECONFIG: %s): %v", kubeconfig, err)
	}
	c, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Skipf("cannot create dynamic client: %v", err)
	}
	return c
}

// TestInfrastructure verifies that the test cluster has both krocodile and
// kardinal-promoter installed and healthy. This test must pass before any
// journey test is meaningful.
func TestInfrastructure(t *testing.T) {
	client := infraClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Verify krocodile Graph CRD is installed
	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}
	_, err := client.Resource(crdGVR).Get(ctx, "graphs.experimental.kro.run", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("krocodile Graph CRD not installed (graphs.experimental.kro.run): %v\n"+
			"Run: make kind-up to create a cluster with krocodile installed.", err)
	}
	t.Log("✅ krocodile Graph CRD installed: graphs.experimental.kro.run")

	_, err = client.Resource(crdGVR).Get(ctx, "graphrevisions.experimental.kro.run", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("krocodile GraphRevision CRD not installed: %v", err)
	}
	t.Log("✅ krocodile GraphRevision CRD installed: graphrevisions.experimental.kro.run")

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
			t.Fatalf("kardinal CRD not installed (%s): %v\nRun: make install", crd, err)
		}
		t.Logf("✅ kardinal CRD installed: %s", crd)
	}

	// Verify graph-controller pod is running in kro-system
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	pods, err := client.Resource(podGVR).Namespace("kro-system").List(ctx, metav1.ListOptions{
		LabelSelector: "app=graph-controller",
	})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("krocodile graph-controller pod not found in kro-system namespace: %v\n"+
			"Run: make install-krocodile", err)
	}
	t.Logf("✅ krocodile graph-controller pod found in kro-system (%d pod(s))", len(pods.Items))
}
