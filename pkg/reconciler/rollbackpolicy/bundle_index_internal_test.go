// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package rollbackpolicy

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

type bundleCounter struct {
	client.Client
	listed []int
}

func (c *bundleCounter) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	err := c.Client.List(ctx, list, opts...)
	if bl, ok := list.(*v1alpha1.BundleList); ok && err == nil {
		c.listed = append(c.listed, len(bl.Items))
	}
	return err
}

// TestExistingRollbackListsOnlyItsBundles (#1654): the legacy rollback
// lookup reads the RollbackPolicy's Pipeline's Bundles through the
// spec.pipeline index, and still finds a rollback without the
// kardinal.io/rollback-from annotation.
func TestExistingRollbackListsOnlyItsBundles(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	legacy := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-rb", Namespace: "ns", Labels: map[string]string{lifecycle.LabelRollback: "true"}},
		Spec: v1alpha1.BundleSpec{Pipeline: "app", Provenance: &v1alpha1.BundleProvenance{RollbackOf: "app-bad"}}}
	objs := []client.Object{legacy, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-bad", Namespace: "ns"}, Spec: v1alpha1.BundleSpec{Pipeline: "app"}}}
	for i := 0; i < 40; i++ {
		objs = append(objs, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("other-%d", i), Namespace: "ns"}, Spec: v1alpha1.BundleSpec{Pipeline: "other"}})
	}
	c := &bundleCounter{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()}
	rp := &v1alpha1.RollbackPolicy{ObjectMeta: metav1.ObjectMeta{Name: "rp", Namespace: "ns"},
		Spec: v1alpha1.RollbackPolicySpec{PipelineName: "app", Environment: "prod", BundleRef: "app-bad"}}
	got, err := (&Reconciler{Client: c}).existingRollback(context.Background(), rp)
	require.NoError(t, err)
	assert.Equal(t, "app-rb", got)
	require.NotEmpty(t, c.listed)
	assert.Equal(t, 2, c.listed[0], "the first Bundle list reads app's Bundles only")
}
