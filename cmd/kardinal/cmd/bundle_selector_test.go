// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestCLI_BundleSelectorOnTheServer (#1654): the CLI talks to the API
// server, and the Bundle CRD's selectable field spec.pipeline filters on the
// server, so `kardinal history` reads one Pipeline's Bundles (the index
// stands in for the server here).
func TestCLI_BundleSelectorOnTheServer(t *testing.T) {
	objs := []sigs_client.Object{
		&v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: "app-v1-dev", Namespace: "default", Labels: map[string]string{"kardinal.io/pipeline": "app"}},
			Spec:   v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-v1", Environment: "dev", StepType: "open-pr"},
			Status: v1alpha1.PromotionStepStatus{State: "Verified"}},
		&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"}, Spec: v1alpha1.BundleSpec{Pipeline: "app"}},
	}
	for i := 0; i < 20; i++ {
		objs = append(objs, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("other-%d", i), Namespace: "default"}, Spec: v1alpha1.BundleSpec{Pipeline: "other"}})
	}
	var listed []int
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(objs...).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl sigs_client.WithWatch, list sigs_client.ObjectList, opts ...sigs_client.ListOption) error {
				err := cl.List(ctx, list, opts...)
				if bl, ok := list.(*v1alpha1.BundleList); ok && err == nil {
					listed = append(listed, len(bl.Items))
				}
				return err
			}}).Build()
	var buf bytes.Buffer
	require.NoError(t, historyFn(t.Context(), &buf, c, "default", "app", "", 20))
	assert.Contains(t, buf.String(), "app-v1")
	require.NotEmpty(t, listed)
	for _, n := range listed {
		assert.Equal(t, 1, n, "every Bundle list of history (steps' retired records, rollbacks) reads app's Bundle only")
	}
}
