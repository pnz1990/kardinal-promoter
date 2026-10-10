// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
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

// TestBundleEventsListOnlyItsBundles (#1654): a hook scoped to one Pipeline
// reads that Pipeline's Bundles through the spec.pipeline index; a hook with
// no Pipeline selector still reads the namespace.
func TestBundleEventsListOnlyItsBundles(t *testing.T) {
	s := k8sruntime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	objs := []client.Object{}
	for i := 0; i < 2; i++ {
		objs = append(objs, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("app-%d", i), Namespace: "ns"},
			Spec: v1alpha1.BundleSpec{Pipeline: "app"}, Status: v1alpha1.BundleStatus{Phase: "Verified"}})
	}
	for i := 0; i < 40; i++ {
		objs = append(objs, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("other-%d", i), Namespace: "ns"},
			Spec: v1alpha1.BundleSpec{Pipeline: "other"}, Status: v1alpha1.BundleStatus{Phase: "Verified"}})
	}
	c := &bundleCounter{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()}
	r := &Reconciler{Client: c}
	events := 0
	require.NoError(t, r.bundleEvents(context.Background(), "ns", "app", func(pendingEvent) { events++ }))
	assert.Equal(t, []int{2}, c.listed)
	assert.Equal(t, 2, events, "one Bundle.Verified per app Bundle")

	c.listed = nil
	require.NoError(t, r.bundleEvents(context.Background(), "ns", "", func(pendingEvent) {}))
	assert.Equal(t, []int{42}, c.listed, "no selector: every Pipeline")
}
