// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// BecameFinal and BundleInstances are the finished-version watch (#2138).
var BecameFinal = becameFinal

// BundleInstances exposes the Bundle-to-instances mapping.
func (r *Reconciler) BundleInstances(ctx context.Context, o client.Object) []reconcile.Request {
	return r.bundleInstances(ctx, o)
}
