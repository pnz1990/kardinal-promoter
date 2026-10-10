// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// fieldIndexer refuses a second index of one name, as a manager's does.
type fieldIndexer struct {
	fields map[string]int
}

func (f *fieldIndexer) IndexField(_ context.Context, _ client.Object, field string, _ client.IndexerFunc) error {
	if f.fields[field] > 0 {
		return errors.New("indexer conflict: " + field)
	}
	f.fields[field]++
	return nil
}

// TestIndexBundlesByPipeline (#1654): the Bundle and Pipeline reconcilers
// both register the spec.pipeline index; the second registration on one
// manager is a no-op, not a conflict, and each manager gets its own.
func TestIndexBundlesByPipeline(t *testing.T) {
	a, b := &fieldIndexer{fields: map[string]int{}}, &fieldIndexer{fields: map[string]int{}}
	for i := 0; i < 2; i++ {
		require.NoError(t, lifecycle.IndexBundlesByPipeline(context.Background(), a))
	}
	require.NoError(t, lifecycle.IndexBundlesByPipeline(context.Background(), b))
	assert.Equal(t, 1, a.fields[lifecycle.IndexBundlePipeline])
	assert.Equal(t, 1, b.fields[lifecycle.IndexBundlePipeline])

	bundle := &v1alpha1.Bundle{Spec: v1alpha1.BundleSpec{Pipeline: "app"}}
	assert.Equal(t, []string{"app"}, lifecycle.BundlePipeline(bundle))
	assert.Nil(t, lifecycle.BundlePipeline(&v1alpha1.Bundle{}))
	assert.Nil(t, lifecycle.BundlePipeline(&v1alpha1.Pipeline{}))
}

// bundleCounter counts the Bundles each BundleList read returns.
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

// pipelineBundles is 2 Bundles of Pipeline "app" and 30 of
// another Pipeline in the same namespace.
func pipelineBundles() []client.Object {
	objs := []client.Object{
		&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "ns"}, Spec: v1alpha1.BundleSpec{Pipeline: "app"}},
		&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-2", Namespace: "ns"}, Spec: v1alpha1.BundleSpec{Pipeline: "app"}},
	}
	for i := 0; i < 30; i++ {
		objs = append(objs, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("other-%d", i), Namespace: "ns"}, Spec: v1alpha1.BundleSpec{Pipeline: "other"}})
	}
	return objs
}

func indexedClient(t *testing.T, index bool) *bundleCounter {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	b := fake.NewClientBuilder().WithScheme(s).WithObjects(pipelineBundles()...)
	if index {
		b = b.WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline)
	}
	return &bundleCounter{Client: b.Build()}
}

// TestListPipelineBundles (#1654): through the index a Pipeline's read
// returns its own Bundles only. A reader with no index registered (a
// manager whose reconcilers forgot IndexBundlesByPipeline) gets an error, not
// a silent namespace-wide list. Only an API server that refuses the field
// selector (BadRequest: Kubernetes 1.30, no selectable field) gets the
// namespace list, filtered here. LoadRejectedArtifacts reads through it.
func TestListPipelineBundles(t *testing.T) {
	c := indexedClient(t, true)
	got, err := lifecycle.ListPipelineBundles(context.Background(), c, "ns", "app")
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Equal(t, []int{2}, c.listed, "the index list returns only app's Bundles")

	c = indexedClient(t, false)
	_, err = lifecycle.ListPipelineBundles(context.Background(), c, "ns", "app")
	require.Error(t, err, "no index registered: an error, not a full list")
	assert.Contains(t, err.Error(), "spec.pipeline")
	assert.Empty(t, c.listed, "no fallback list")

	c = indexedClient(t, false)
	c.Client = interceptor.NewClient(c.Client.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if (&client.ListOptions{}).ApplyOptions(opts).FieldSelector != nil {
				return apierrors.NewBadRequest(`field label not supported: spec.pipeline`)
			}
			return cl.List(ctx, list, opts...)
		}})
	got, err = lifecycle.ListPipelineBundles(context.Background(), c, "ns", "app")
	require.NoError(t, err)
	assert.Len(t, got, 2, "BadRequest (an API server without the selectable field): the namespace list, filtered")
	assert.Equal(t, []int{32}, c.listed)

	c = indexedClient(t, true)
	_, err = lifecycle.LoadRejectedArtifacts(context.Background(), c, "ns", "app")
	require.NoError(t, err)
	assert.Equal(t, []int{2}, c.listed, "LoadRejectedArtifacts reads app's Bundles only")
}

// TestListPromotionStepsReadsItsBundles (#1654): one Pipeline's steps (with
// retired ones) read that Pipeline's Bundles only.
func TestListPromotionStepsReadsItsBundles(t *testing.T) {
	c := indexedClient(t, true)
	_, err := lifecycle.ListPromotionSteps(context.Background(), c, "ns", client.MatchingLabels{lifecycle.LabelPipeline: "app"})
	require.NoError(t, err)
	assert.Equal(t, []int{2}, c.listed)
}

// TestPlanPromoteReadsItsBundles (#1654): planning a promotion reads the
// Pipeline's Bundles only (its steps' retired records and its candidates).
func TestPlanPromoteReadsItsBundles(t *testing.T) {
	c := indexedClient(t, true)
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "ns"}, Spec: v1alpha1.PipelineSpec{
		Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod", DependsOn: []string{"test"}}}}}
	require.NoError(t, c.Create(context.Background(), p))
	_, _ = lifecycle.PlanPromote(context.Background(), c, lifecycle.PromoteRequest{Namespace: "ns", Pipeline: "app", Environment: "prod"})
	require.NotEmpty(t, c.listed)
	for _, n := range c.listed {
		assert.Equal(t, 2, n, "a Bundle list returned other Pipelines' Bundles")
	}
}
