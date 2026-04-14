// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package pipeline implements the PipelineReconciler which watches Pipeline
// objects, validates their environment configuration, and sets status conditions
// and status.phase based on active PromotionStep states.
package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Reconciler watches Pipeline objects, validates them, and sets status.conditions
// and status.phase.
type Reconciler struct {
	client.Client
}

// Reconcile is called whenever a Pipeline or a PromotionStep in its namespace changes.
// It validates environment configuration, derives status.phase from active PromotionSteps,
// and sets the Ready condition.
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

	// Determine the desired condition from pipeline spec validation.
	desiredReason, desiredMsg := r.validate(&p)

	// Derive status.phase from active PromotionStep states.
	// List all PromotionSteps in the pipeline's namespace that belong to this pipeline.
	// This is a Watch-node pattern: we read PromotionStep CRD status (written by the
	// PromotionStep reconciler) and write only our own CRD status (Pipeline.status.phase).
	var stepList kardinalv1alpha1.PromotionStepList
	if err := r.List(ctx, &stepList,
		client.InNamespace(p.Namespace),
		client.MatchingFields{"spec.pipelineName": p.Name},
	); err != nil {
		// Non-fatal: if list fails, use Unknown phase.
		log.Warn().Err(err).Msg("failed to list PromotionSteps for phase derivation, using Unknown")
	}

	desiredPhase := DerivePhase(stepList.Items)

	// Compute aggregate deployment metrics from Bundles + PromotionSteps.
	// Graph-first: reads only CRD status fields written by their own reconcilers.
	// Writes only to Pipeline.status.deploymentMetrics (our own CRD).
	var bundleList kardinalv1alpha1.BundleList
	if listErr := r.List(ctx, &bundleList, client.InNamespace(p.Namespace)); listErr != nil {
		log.Warn().Err(listErr).Msg("failed to list Bundles for metrics computation, skipping")
	}
	desiredMetrics := ComputeDeploymentMetrics(&p, bundleList.Items, stepList.Items, time.Now().UTC())

	// Idempotency: only patch if something changed.
	condMatch := conditionMatches(p.Status.Conditions, desiredReason)
	phaseMatch := p.Status.Phase == desiredPhase
	metricsMatch := deploymentMetricsEqual(p.Status.DeploymentMetrics, desiredMetrics)
	if condMatch && phaseMatch && metricsMatch {
		log.Debug().
			Str("reason", desiredReason).
			Str("phase", desiredPhase).
			Msg("pipeline status already correct, skipping")
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(p.DeepCopy())
	p.Status.Phase = desiredPhase
	p.Status.Conditions = []metav1.Condition{
		{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             desiredReason,
			Message:            desiredMsg,
			LastTransitionTime: metav1.Now(),
		},
	}
	p.Status.DeploymentMetrics = desiredMetrics

	if err := r.Status().Patch(ctx, &p, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch pipeline status: %w", err)
	}

	log.Info().
		Str("reason", desiredReason).
		Str("phase", desiredPhase).
		Int("environments", len(p.Spec.Environments)).
		Msg("pipeline status updated")

	return ctrl.Result{}, nil
}

// DerivePhase computes the Pipeline.status.phase from the given PromotionStep list:
//   - "Unknown"  — no PromotionSteps exist yet (pipeline initializing or no bundles)
//   - "Degraded" — at least one PromotionStep is in Failed state
//   - "Ready"    — all PromotionSteps are in Verified state (at least one step)
//
// Phase derivation is based on the MOST RECENT step per environment: if multiple
// bundles have steps for the same env, only the newest one is used.
func DerivePhase(steps []kardinalv1alpha1.PromotionStep) string {
	if len(steps) == 0 {
		return "Unknown"
	}

	// Track the most recent step per pipeline+env (same logic as FormatPipelineTable).
	type envKey struct{ pipeline, env string }
	bestStep := make(map[envKey]*kardinalv1alpha1.PromotionStep)
	for i := range steps {
		s := &steps[i]
		key := envKey{s.Spec.PipelineName, s.Spec.Environment}
		existing, ok := bestStep[key]
		if !ok || s.CreationTimestamp.After(existing.CreationTimestamp.Time) {
			bestStep[key] = s
		}
	}

	hasFailed := false
	allVerified := true
	for _, s := range bestStep {
		state := s.Status.State
		if state == "Failed" {
			hasFailed = true
		}
		if state != "Verified" {
			allVerified = false
		}
	}

	switch {
	case hasFailed:
		return "Degraded"
	case allVerified:
		return "Ready"
	default:
		return "Unknown"
	}
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
// It watches Pipeline objects AND PromotionSteps (to update pipeline phase when step
// states change). The PromotionStep watch maps each step to its pipeline via
// spec.pipelineName, triggering a reconcile of the pipeline.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Index PromotionSteps by spec.pipelineName for efficient lookup in Reconcile.
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&kardinalv1alpha1.PromotionStep{},
		"spec.pipelineName",
		func(obj client.Object) []string {
			s, ok := obj.(*kardinalv1alpha1.PromotionStep)
			if !ok || s.Spec.PipelineName == "" {
				return nil
			}
			return []string{s.Spec.PipelineName}
		},
	); err != nil {
		return fmt.Errorf("index PromotionStep by spec.pipelineName: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.Pipeline{}).
		// Enqueue the pipeline named by spec.pipelineName whenever a PromotionStep changes.
		Watches(&kardinalv1alpha1.PromotionStep{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []ctrl.Request {
				s, ok := obj.(*kardinalv1alpha1.PromotionStep)
				if !ok || s.Spec.PipelineName == "" {
					return nil
				}
				return []ctrl.Request{{
					NamespacedName: client.ObjectKey{
						Name:      s.Spec.PipelineName,
						Namespace: s.Namespace,
					},
				}}
			}),
		).
		Complete(r)
}

// deploymentMetricsEqual returns true when a and b represent the same metrics.
// We compare the scalar fields; ComputedAt is intentionally excluded from the
// equality check to avoid patching on every reconcile when no data changed.
// SampleSize IS included: if more Bundles become available, we want to recompute.
func deploymentMetricsEqual(a, b *kardinalv1alpha1.PipelineDeploymentMetrics) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.RolloutsLast30Days == b.RolloutsLast30Days &&
		a.P50CommitToProdMinutes == b.P50CommitToProdMinutes &&
		a.P90CommitToProdMinutes == b.P90CommitToProdMinutes &&
		a.AutoRollbackRateMillis == b.AutoRollbackRateMillis &&
		a.OperatorInterventionRateMillis == b.OperatorInterventionRateMillis &&
		a.StaleProdDays == b.StaleProdDays &&
		a.SampleSize == b.SampleSize
}
