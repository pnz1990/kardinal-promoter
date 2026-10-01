// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package objectgone ends a reconcile quietly when the object it reconciles
// was deleted while it ran.
//
// A reconciler reads its object from the cache, works on it, then writes its
// status or metadata. When the object is deleted in between, the write fails
// with NotFound. Returning that error logs a "Reconciler error" and requeues a
// request that finds nothing: the object is gone, so there is nothing left to
// do or retry. Reconcile turns that error into an empty result.
package objectgone

import (
	"context"
	"errors"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Is reports whether err, or an error it wraps, is the API server's NotFound
// for the object called name of resource gr. A NotFound for any other object,
// such as a Pipeline a Bundle names or a Secret, is not the object being gone,
// and Is returns false for it. The API server reports the resource, not the
// kind, in details.kind.
func Is(err error, gr schema.GroupResource, name string) bool {
	if !apierrors.IsNotFound(err) {
		return false
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return false
	}
	d := status.Status().Details
	return d != nil && d.Name == name && d.Group == gr.Group && d.Kind == gr.Resource
}

// Reconcile calls fn with req. When fn fails because the object of req, a gr,
// is gone (Is), Reconcile logs that at debug and returns an empty result and
// no error. Any other result and error are returned as they are.
func Reconcile(ctx context.Context, req ctrl.Request, gr schema.GroupResource,
	fn reconcile.Func) (ctrl.Result, error) {
	res, err := fn(ctx, req)
	if err != nil && Is(err, gr, req.Name) {
		zerolog.Ctx(ctx).Debug().Err(err).
			Str("resource", gr.String()).
			Str("name", req.Name).
			Str("namespace", req.Namespace).
			Msg("deleted while it was reconciled; nothing left to do")
		return ctrl.Result{}, nil
	}
	return res, err
}
