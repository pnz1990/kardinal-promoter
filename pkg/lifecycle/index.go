// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"fmt"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// IndexBundlePipeline is the Bundle field index on spec.pipeline: list a
// Pipeline's Bundles with client.MatchingFields{IndexBundlePipeline: name}
// instead of every Bundle of the namespace (#1654).
const IndexBundlePipeline = "spec.pipeline"

// BundlePipeline is the IndexBundlePipeline index function.
func BundlePipeline(obj client.Object) []string {
	b, ok := obj.(*v1alpha1.Bundle)
	if !ok || b.Spec.Pipeline == "" {
		return nil
	}
	return []string{b.Spec.Pipeline}
}

// registered holds, per field indexer, the result of registering
// IndexBundlePipeline: a manager refuses a second index of the same name,
// and both the Bundle and the Pipeline reconcilers need this one.
var registered sync.Map // client.FieldIndexer -> *indexOnce

type indexOnce struct {
	once sync.Once
	err  error
}

// IndexBundlesByPipeline registers IndexBundlePipeline on indexer once;
// later calls with the same indexer return the first call's result.
func IndexBundlesByPipeline(ctx context.Context, indexer client.FieldIndexer) error {
	v, _ := registered.LoadOrStore(indexer, &indexOnce{})
	o := v.(*indexOnce)
	o.once.Do(func() {
		if err := indexer.IndexField(ctx, &v1alpha1.Bundle{}, IndexBundlePipeline, BundlePipeline); err != nil {
			o.err = fmt.Errorf("index Bundle by spec.pipeline: %w", err)
		}
	})
	return o.err
}
