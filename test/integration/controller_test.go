// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

//go:build integration

// Package integration contains integration tests for the kardinal-promoter controller.
// These tests run reconcilers in-process using a fake client, verifying that
// Bundle and Pipeline objects transition to the expected status within 5 seconds.
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	bundlereconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	pipelinereconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// buildScheme returns a Scheme with both client-go and kardinal types registered.
func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s), "clientgoscheme.AddToScheme must not fail")
	require.NoError(t, kardinalv1alpha1.AddToScheme(s), "kardinalv1alpha1.AddToScheme must not fail")
	return s
}

// TestControllerIntegration verifies that:
// - Bundle.status.phase == "Available" after reconciliation
// - Pipeline.status.conditions[0].type == "Ready"
// - Pipeline.status.conditions[0].status == "False"
//
// This test runs the reconcilers in-process using a fake client.
// No real Kubernetes cluster is required.
func TestControllerIntegration(t *testing.T) {
	const (
		namespace    = "default"
		pipelineName = "nginx-demo"
		bundleName   = "nginx-demo-v1-29-0"
		timeout      = 5 * time.Second
		pollInterval = 50 * time.Millisecond
	)

	scheme := buildScheme(t)

	// Create a Pipeline object
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pipelineName,
			Namespace: namespace,
		},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{
				URL:    "https://github.com/example/nginx-demo",
				Branch: "main",
			},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{
					Name: "dev",
				},
				{
					Name:      "prod",
					DependsOn: []string{"dev"},
					Approval:  "pr-review",
				},
			},
		},
	}

	// Create a Bundle object
	bundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bundleName,
			Namespace: namespace,
			Labels: map[string]string{
				"kardinal.io/pipeline": pipelineName,
			},
		},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: pipelineName,
			Images: []kardinalv1alpha1.ImageRef{
				{
					Repository: "ghcr.io/example/nginx-demo",
					Tag:        "1.29.0",
					Digest:     "sha256:replace-with-actual-digest",
				},
			},
		},
	}

	// Build fake client with both objects pre-created
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pipeline, bundle).
		WithStatusSubresource(pipeline, bundle).
		Build()

	ctx := context.Background()

	t.Run("BundleReconciler sets phase to Available", func(t *testing.T) {
		r := &bundlereconciler.Reconciler{Client: fakeClient}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      bundleName,
				Namespace: namespace,
			},
		}

		// Reconcile should succeed; it returns Requeue:true to immediately
		// advance the bundle from Available to Promoting on the next cycle.
		result, err := r.Reconcile(ctx, req)
		require.NoError(t, err, "BundleReconciler.Reconcile must not return error")
		// Requeue:true is expected — the reconciler signals immediate re-queue
		// after setting Available phase so it can advance to Promoting.
		assert.True(t, result.Requeue, "expected Requeue:true after Available phase set")

		// Verify within 5 seconds
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			var got kardinalv1alpha1.Bundle
			if err := fakeClient.Get(ctx, client.ObjectKey{Name: bundleName, Namespace: namespace}, &got); err != nil {
				t.Logf("get bundle: %v", err)
				time.Sleep(pollInterval)
				continue
			}
			if got.Status.Phase == "Available" {
				return // success
			}
			time.Sleep(pollInterval)
		}
		// Re-read for assertion message
		var got kardinalv1alpha1.Bundle
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Name: bundleName, Namespace: namespace}, &got))
		assert.Equal(t, "Available", got.Status.Phase, "Bundle.status.phase must be 'Available' within 5 seconds")
	})

	t.Run("BundleReconciler is idempotent", func(t *testing.T) {
		r := &bundlereconciler.Reconciler{Client: fakeClient}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      bundleName,
				Namespace: namespace,
			},
		}

		// Reconcile a second time — should be a no-op
		result, err := r.Reconcile(ctx, req)
		require.NoError(t, err, "second BundleReconciler.Reconcile must not return error")
		assert.Equal(t, ctrl.Result{}, result, "expected empty Result on second reconcile")

		var got kardinalv1alpha1.Bundle
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Name: bundleName, Namespace: namespace}, &got))
		assert.Equal(t, "Available", got.Status.Phase, "Bundle phase must still be 'Available' after second reconcile")
	})

	t.Run("PipelineReconciler sets Ready condition to False", func(t *testing.T) {
		r := &pipelinereconciler.Reconciler{Client: fakeClient}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      pipelineName,
				Namespace: namespace,
			},
		}

		// Reconcile should succeed
		result, err := r.Reconcile(ctx, req)
		require.NoError(t, err, "PipelineReconciler.Reconcile must not return error")
		assert.Equal(t, ctrl.Result{}, result, "expected empty Result")

		// Verify within 5 seconds
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			var got kardinalv1alpha1.Pipeline
			if err := fakeClient.Get(ctx, client.ObjectKey{Name: pipelineName, Namespace: namespace}, &got); err != nil {
				t.Logf("get pipeline: %v", err)
				time.Sleep(pollInterval)
				continue
			}
			if len(got.Status.Conditions) > 0 {
				cond := got.Status.Conditions[0]
				if cond.Type == "Ready" && cond.Status == metav1.ConditionFalse {
					return // success
				}
			}
			time.Sleep(pollInterval)
		}
		// Re-read for assertion message
		var got kardinalv1alpha1.Pipeline
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Name: pipelineName, Namespace: namespace}, &got))
		require.NotEmpty(t, got.Status.Conditions, "Pipeline must have at least one condition")
		assert.Equal(t, "Ready", got.Status.Conditions[0].Type, "Pipeline.status.conditions[0].type must be 'Ready'")
		assert.Equal(t, metav1.ConditionFalse, got.Status.Conditions[0].Status, "Pipeline.status.conditions[0].status must be 'False'")
	})

	t.Run("PipelineReconciler is idempotent", func(t *testing.T) {
		r := &pipelinereconciler.Reconciler{Client: fakeClient}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      pipelineName,
				Namespace: namespace,
			},
		}

		// Reconcile a second time — should be a no-op
		result, err := r.Reconcile(ctx, req)
		require.NoError(t, err, "second PipelineReconciler.Reconcile must not return error")
		assert.Equal(t, ctrl.Result{}, result, "expected empty Result on second reconcile")

		var got kardinalv1alpha1.Pipeline
		require.NoError(t, fakeClient.Get(ctx, client.ObjectKey{Name: pipelineName, Namespace: namespace}, &got))
		require.NotEmpty(t, got.Status.Conditions, "Pipeline must still have conditions after second reconcile")
		assert.Equal(t, "Ready", got.Status.Conditions[0].Type, "condition type must still be 'Ready'")
		assert.Equal(t, metav1.ConditionFalse, got.Status.Conditions[0].Status, "condition status must still be 'False'")
	})
}
