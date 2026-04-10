// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package pipeline implements the PipelineReconciler which watches Pipeline
// objects, validates their environment configuration, and sets status conditions.
package pipeline

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Reconciler watches Pipeline objects, validates them, and sets status.conditions.
type Reconciler struct {
	client.Client
}

// Reconcile is called whenever a Pipeline is created, updated, or deleted.
// It validates environment configuration and sets Ready condition accordingly.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("pipeline", req.Name).
		Str("namespace", req.Namespace).
		Logger()

	var p kardinalv1alpha1.Pipeline
	if err := r.Get(ctx, req.NamespacedName, &p); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debug().Msg("pipeline not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get pipeline: %w", err)
	}

	// Determine the desired condition
	desiredReason, desiredMsg := r.validate(&p)

	// Idempotency: if condition already matches desired state, skip patching
	if conditionMatches(p.Status.Conditions, desiredReason) {
		log.Debug().Str("reason", desiredReason).Msg("pipeline condition already correct, skipping")
		return ctrl.Result{}, nil
	}

	status := metav1.ConditionFalse
	patch := client.MergeFrom(p.DeepCopy())
	p.Status.Conditions = []metav1.Condition{
		{
			Type:               "Ready",
			Status:             status,
			Reason:             desiredReason,
			Message:            desiredMsg,
			LastTransitionTime: metav1.Now(),
		},
	}

	if err := r.Status().Patch(ctx, &p, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch pipeline status: %w", err)
	}

	log.Info().
		Str("reason", desiredReason).
		Int("environments", len(p.Spec.Environments)).
		Msg("pipeline status updated")

	return ctrl.Result{}, nil
}

// validate checks pipeline invariants and returns (reason, message).
func (r *Reconciler) validate(p *kardinalv1alpha1.Pipeline) (string, string) {
	// Check for duplicate environment names
	seen := make(map[string]struct{}, len(p.Spec.Environments))
	for _, env := range p.Spec.Environments {
		if _, exists := seen[env.Name]; exists {
			return "ValidationFailed", fmt.Sprintf("duplicate environment name %q in pipeline spec", env.Name)
		}
		seen[env.Name] = struct{}{}
	}

	// Check dependsOn references valid environments
	for _, env := range p.Spec.Environments {
		for _, dep := range env.DependsOn {
			if _, exists := seen[dep]; !exists {
				return "ValidationFailed", fmt.Sprintf(
					"environment %q has dependsOn %q which does not exist in this pipeline",
					env.Name, dep,
				)
			}
		}
	}

	return "Initializing", "Pipeline initialized, awaiting first Bundle"
}

// conditionMatches returns true if conditions contains a Ready condition with
// the given reason — used for idempotency checks.
func conditionMatches(conditions []metav1.Condition, reason string) bool {
	for _, c := range conditions {
		if c.Type == "Ready" && c.Reason == reason {
			return true
		}
	}
	return false
}

// SetupWithManager registers the PipelineReconciler with the controller-runtime Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.Pipeline{}).
		Complete(r)
}
