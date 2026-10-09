// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestBundleReconciler_ProviderRefRetry: a providerRef that cannot be used
// yet keeps the Bundle Available with the reason, and the Bundle is looked at
// again on a fixed short interval (not the error backoff), so a provider
// created later is picked up soon.
func TestBundleReconciler_ProviderRefRetry(t *testing.T) {
	for _, sentinel := range []error{scm.ErrProviderGone, scm.ErrRepositoryNotAllowed, scm.ErrNamespaceNotAllowed} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			pipeline := &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
				Spec:       kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}}},
			}
			b := &kardinalv1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
				Spec:       kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
				Status:     kardinalv1alpha1.BundleStatus{Phase: "Available"},
			}
			c := indexedBuilder(newScheme()).WithObjects(pipeline, b).WithStatusSubresource(b).Build()
			r := &bundle.Reconciler{Client: c, Translator: &mockTranslator{err: fmt.Errorf("spec.git.providerRef: x: %w", sentinel)}}
			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "app-v1", Namespace: "default"}})
			require.NoError(t, err)
			assert.Positive(t, res.RequeueAfter)

			var got kardinalv1alpha1.Bundle
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "app-v1", Namespace: "default"}, &got))
			assert.Equal(t, "Available", got.Status.Phase)
			ready := findCondition(got.Status.Conditions, "Ready")
			require.NotNil(t, ready)
			assert.Equal(t, "TranslationError", ready.Reason)
			assert.Contains(t, ready.Message, sentinel.Error())
		})
	}
}
