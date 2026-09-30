// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// The GraphChecker main.go wires in must also read Graph status, or the
// GraphAccepted/GraphReady conditions are never mirrored.
var _ graphReader = (*graph.GraphClient)(nil)

// Exported for the watch-mapping tests in package bundle_test.
var (
	BundleLabelMapper   = bundleLabelMapper
	BundlePhaseChanged  = bundlePhaseChanged
	BundlePipelineIndex = bundlePipelineIndex
)

// WaitingSiblings exposes the sibling-Bundle watch mapping.
func (r *Reconciler) WaitingSiblings(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.waitingSiblings(ctx, obj)
}

// PipelineBundles exposes the Pipeline watch mapping.
func (r *Reconciler) PipelineBundles(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.pipelineBundles(ctx, obj)
}
