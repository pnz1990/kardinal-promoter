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

// ListPipelineBundles lists the Bundles of pipeline in ns by spec.pipeline,
// so a Pipeline's read costs its own Bundles, not every Bundle of the
// namespace (#1654). A cached reader answers from the IndexBundlePipeline
// index; the API server from the CRD's selectable field. An error, such as
// a cache with no index registered, is returned, so a missing index is a bug
// that shows rather than a silent namespace-wide list.
func ListPipelineBundles(ctx context.Context, c client.Reader, ns, pipeline string) ([]v1alpha1.Bundle, error) {
	var list v1alpha1.BundleList
	if err := c.List(ctx, &list, client.InNamespace(ns), client.MatchingFields{IndexBundlePipeline: pipeline}); err != nil {
		return nil, fmt.Errorf("list bundles of pipeline %s: %w", pipeline, err)
	}
	return list.Items, nil
}

// onceKey identifies one field index on one indexer.
type onceKey struct {
	indexer client.FieldIndexer
	kind    string
	field   string
}

var indexed sync.Map // onceKey -> *indexOnce

// IndexOnce registers field on obj's kind with indexer once; later calls for
// the same indexer, kind and field return the first call's result. A manager
// refuses a second index of the same name, and several reconcilers read
// some indexes (the agent budget indexes: the budget reconciler, the step
// hold and the gates' budget.* context).
func IndexOnce(ctx context.Context, indexer client.FieldIndexer, obj client.Object, field string, fn client.IndexerFunc) error {
	v, _ := indexed.LoadOrStore(onceKey{indexer, fmt.Sprintf("%T", obj), field}, &indexOnce{})
	o := v.(*indexOnce)
	o.once.Do(func() {
		if err := indexer.IndexField(ctx, obj, field, fn); err != nil {
			o.err = fmt.Errorf("index %T by %s: %w", obj, field, err)
		}
	})
	return o.err
}
