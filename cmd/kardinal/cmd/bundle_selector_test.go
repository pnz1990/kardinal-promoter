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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestCLI_BundleSelectorBothServerPaths (#1654): the CLI talks to the API
// server. On Kubernetes 1.31+ the Bundle CRD's selectable field spec.pipeline
// filters on the server, so `kardinal history` reads one Pipeline's Bundles
// (the index stands in for the server here). On 1.30 the server refuses the
// field selector with BadRequest and the CLI lists the namespace and filters
// it. Both paths print the same history.
func TestCLI_BundleSelectorBothServerPaths(t *testing.T) {
	objs := []sigs_client.Object{
		&v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: "app-v1-dev", Namespace: "default", Labels: map[string]string{"kardinal.io/pipeline": "app"}},
			Spec:   v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "app-v1", Environment: "dev", StepType: "open-pr"},
			Status: v1alpha1.PromotionStepStatus{State: "Verified"}},
		&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"}, Spec: v1alpha1.BundleSpec{Pipeline: "app"}},
	}
	for i := 0; i < 20; i++ {
		objs = append(objs, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("other-%d", i), Namespace: "default"}, Spec: v1alpha1.BundleSpec{Pipeline: "other"}})
	}
	for _, tc := range []struct {
		name       string
		selectable bool // the server supports the selectable field
		wantListed int  // Bundles each Bundle list read
	}{
		{"1.31+: filtered on the server", true, 1},
		{"1.30: BadRequest, namespace list filtered here", false, 21},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var listed []int
			b := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(objs...)
			if tc.selectable {
				b = b.WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline)
			}
			c := b.WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, cl sigs_client.WithWatch, list sigs_client.ObjectList, opts ...sigs_client.ListOption) error {
					if _, ok := list.(*v1alpha1.BundleList); ok && !tc.selectable && (&sigs_client.ListOptions{}).ApplyOptions(opts).FieldSelector != nil {
						return apierrors.NewBadRequest(`Unable to find "kardinal.io/v1alpha1, Resource=bundles" that match label selector "", field selector "spec.pipeline=app": field label not supported: spec.pipeline`)
					}
					err := cl.List(ctx, list, opts...)
					if bl, ok := list.(*v1alpha1.BundleList); ok && err == nil {
						listed = append(listed, len(bl.Items))
					}
					return err
				}}).Build()
			var buf bytes.Buffer
			require.NoError(t, historyFn(&buf, c, "default", "app", "", 20))
			assert.Contains(t, buf.String(), "app-v1")
			require.NotEmpty(t, listed)
			for _, n := range listed {
				assert.Equal(t, tc.wantListed, n, "every Bundle list of history (steps' retired records, rollbacks)")
			}
		})
	}
}
