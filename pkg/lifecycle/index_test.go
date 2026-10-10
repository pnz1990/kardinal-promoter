// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
