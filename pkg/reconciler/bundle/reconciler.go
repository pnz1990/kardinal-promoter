// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package bundle implements the BundleReconciler which watches Bundle objects
// and sets status.phase = Available on newly-created Bundles.
package bundle

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Reconciler watches Bundle objects and sets status.phase = Available.
type Reconciler struct {
	client.Client
}

// Reconcile is called whenever a Bundle is created, updated, or deleted.
// It sets status.phase = Available if not already set (idempotent).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("bundle", req.Name).
		Str("namespace", req.Namespace).
		Logger()

	var b kardinalv1alpha1.Bundle
	if err := r.Get(ctx, req.NamespacedName, &b); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debug().Msg("bundle not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get bundle: %w", err)
	}

	// Idempotency: skip if phase already set
	if b.Status.Phase != "" {
		log.Debug().Str("phase", b.Status.Phase).Msg("bundle phase already set, skipping")
		return ctrl.Result{}, nil
	}

	// Set Available phase
	patch := client.MergeFrom(b.DeepCopy())
	b.Status.Phase = "Available"

	if err := r.Status().Patch(ctx, &b, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch bundle status: %w", err)
	}

	log.Info().
		Str("phase", "Available").
		Str("type", b.Spec.Type).
		Str("pipeline", b.Spec.Pipeline).
		Msg("bundle phase set to Available")

	return ctrl.Result{}, nil
}

// SetupWithManager registers the BundleReconciler with the controller-runtime Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.Bundle{}).
		Complete(r)
}
