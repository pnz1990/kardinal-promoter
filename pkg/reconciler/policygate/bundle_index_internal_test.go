// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

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

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

type bundleCounter struct {
	client.Client
	listed []int
}

func (c *bundleCounter) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	err := c.Client.List(ctx, list, opts...)
	if bl, ok := list.(*kardinalv1alpha1.BundleList); ok && err == nil {
		c.listed = append(c.listed, len(bl.Items))
	}
	return err
}

// TestUpstreamHistoryListsOnlyItsBundles (#1654): a gate's upstream history
// reads its Pipeline's Bundles through the spec.pipeline index, not every
// Bundle of the namespace, on every gate evaluation.
func TestUpstreamHistoryListsOnlyItsBundles(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	objs := []client.Object{}
	for i := 0; i < 3; i++ {
		objs = append(objs, &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("app-%d", i), Namespace: "ns"},
			Spec: kardinalv1alpha1.BundleSpec{Pipeline: "app"}})
	}
	for i := 0; i < 40; i++ {
		objs = append(objs, &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("other-%d", i), Namespace: "ns"},
			Spec: kardinalv1alpha1.BundleSpec{Pipeline: "other"}})
	}
	c := &bundleCounter{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()}
	current := objs[0].(*kardinalv1alpha1.Bundle)
	_, err := (&Reconciler{Client: c}).buildUpstreamContextWithHistory(context.Background(), "ns", "app", current)
	require.NoError(t, err)
	assert.Equal(t, []int{3}, c.listed)
}
