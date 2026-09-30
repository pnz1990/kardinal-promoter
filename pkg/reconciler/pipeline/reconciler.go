// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package pipeline implements the PipelineReconciler which watches Pipeline
// objects, validates their environment configuration, and sets status conditions
// and status.phase based on active PromotionStep states.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// Ready condition reasons.
const (
	reasonValid            = "Valid"
	reasonValidationFailed = "ValidationFailed"
)

// The Paused condition is set while spec.paused is true and removed when it is
// not. True/FreezeGateActive: the freeze gate holds new promotions.
// False/FreezeGateNameConflict: a user PolicyGate has the freeze gate's name,
// so nothing holds the pipeline until that gate is renamed or deleted.
const (
	conditionPaused              = "Paused"
	reasonFreezeGateActive       = "FreezeGateActive"
	reasonFreezeGateNameConflict = "FreezeGateNameConflict"
)

// Reconciler watches Pipeline objects, validates them, and sets status.conditions
// and status.phase.
type Reconciler struct {
	client.Client
}

// Reconcile is called whenever a Pipeline, one of its PromotionSteps or its
// freeze gate changes. It validates the spec, converges the freeze gate to
// spec.paused, derives status.phase from the PromotionSteps and sets the Ready
// condition.
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

	// spec.paused is the request; the freeze gate is what the PromotionStep
	// reconciler reads. Converging here makes a plain spec edit pause too, and
	// recreates a freeze gate deleted by hand while the pipeline is paused.
	desiredPaused, err := r.convergeFreezeGate(ctx, &p)
	if err != nil {
		return ctrl.Result{}, err
	}
	if desiredPaused != nil && desiredPaused.Status == metav1.ConditionFalse {
		log.Warn().Str("reason", desiredPaused.Reason).Msg(desiredPaused.Message)
	}

	desired := r.validate(&p)

	// Derive status.phase from active PromotionStep states.
	// This is a Watch-node pattern: we read PromotionStep CRD status (written by the
	// PromotionStep reconciler) and write only our own CRD status (Pipeline.status.phase).
	// A failed read returns the error without patching, so a transient apiserver
	// or cache error never overwrites a good phase and metrics.
	var stepList kardinalv1alpha1.PromotionStepList
	if err := r.List(ctx, &stepList,
		client.InNamespace(p.Namespace),
		client.MatchingFields{"spec.pipelineName": p.Name},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list promotion steps of pipeline %s: %w", p.Name, err)
	}

	desiredPhase := DerivePhase(stepList.Items)

	// Compute aggregate deployment metrics from Bundles + PromotionSteps.
	// Graph-first: reads only CRD status fields written by their own reconcilers.
	// Writes only to Pipeline.status.deploymentMetrics (our own CRD).
	var bundleList kardinalv1alpha1.BundleList
	if err := r.List(ctx, &bundleList, client.InNamespace(p.Namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("list bundles of pipeline %s: %w", p.Name, err)
	}
	desiredMetrics := ComputeDeploymentMetrics(&p, bundleList.Items, stepList.Items, time.Now().UTC())

	// Idempotency: only patch if something changed.
	condMatch := conditionMatches(p.Status.Conditions, desired)
	phaseMatch := p.Status.Phase == desiredPhase
	metricsMatch := deploymentMetricsEqual(p.Status.DeploymentMetrics, desiredMetrics)
	pausedMatch := meta.FindStatusCondition(p.Status.Conditions, conditionPaused) == nil
	if desiredPaused != nil {
		pausedMatch = conditionMatches(p.Status.Conditions, *desiredPaused)
	}
	if condMatch && phaseMatch && metricsMatch && pausedMatch {
		log.Debug().
			Str("reason", desired.Reason).
			Str("phase", desiredPhase).
			Msg("pipeline status already correct, skipping")
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(p.DeepCopy())
	p.Status.Phase = desiredPhase
	// SetStatusCondition keeps lastTransitionTime unless the status changes.
	meta.SetStatusCondition(&p.Status.Conditions, desired)
	if desiredPaused != nil {
		meta.SetStatusCondition(&p.Status.Conditions, *desiredPaused)
	} else {
		meta.RemoveStatusCondition(&p.Status.Conditions, conditionPaused)
	}
	p.Status.DeploymentMetrics = desiredMetrics

	if err := r.Status().Patch(ctx, &p, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("patch pipeline status: %w", err)
	}

	log.Info().
		Str("reason", desired.Reason).
		Str("phase", desiredPhase).
		Int("environments", len(p.Spec.Environments)).
		Msg("pipeline status updated")

	return ctrl.Result{}, nil
}

// convergeFreezeGate creates the freeze gate of a paused pipeline and deletes
// it when the pipeline is not paused. It returns the desired Paused condition,
// nil when the pipeline is not paused. A user PolicyGate with the freeze gate's
// name is not an error to retry: it is reported as Paused=False until the user
// renames or deletes it (the PolicyGate watch then re-enqueues the Pipeline).
func (r *Reconciler) convergeFreezeGate(ctx context.Context, p *kardinalv1alpha1.Pipeline) (*metav1.Condition, error) {
	if !p.Spec.Paused {
		if err := lifecycle.RemoveFreezeGate(ctx, r.Client, p.Namespace, p.Name); err != nil {
			return nil, fmt.Errorf("resume: %w", err)
		}
		return nil, nil
	}
	err := lifecycle.EnsureFreezeGate(ctx, r.Client, p)
	switch {
	case errors.Is(err, lifecycle.ErrConflict):
		return &metav1.Condition{
			Type: conditionPaused, Status: metav1.ConditionFalse, Reason: reasonFreezeGateNameConflict,
			Message: err.Error(), ObservedGeneration: p.Generation,
		}, nil
	case err != nil:
		return nil, fmt.Errorf("pause: %w", err)
	}
	return &metav1.Condition{
		Type: conditionPaused, Status: metav1.ConditionTrue, Reason: reasonFreezeGateActive,
		Message:            fmt.Sprintf("new promotions are held by PolicyGate %s", lifecycle.FreezeGateName(p.Name)),
		ObservedGeneration: p.Generation,
	}, nil
}

// pipelineForFreezeGate maps a PolicyGate named freeze-<pipeline> to that
// Pipeline. Mapping by name, not owner, also re-enqueues the Pipeline when a
// user gate blocking its freeze gate is renamed or deleted.
func pipelineForFreezeGate(_ context.Context, obj client.Object) []ctrl.Request {
	name, ok := strings.CutPrefix(obj.GetName(), lifecycle.FreezeGateName(""))
	if !ok || name == "" {
		return nil
	}
	return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: name}}}
}

// Step states that DerivePhase reports as Degraded.
var degradedStates = map[string]bool{
	"Failed":         true,
	"AbortedByAlarm": true,
	"RollingBack":    true,
}

// DerivePhase computes the Pipeline.status.phase from the given PromotionStep list:
//   - "Unknown"  — no PromotionSteps exist yet, or the newest Bundle is still promoting somewhere
//   - "Degraded" — the newest Bundle in some environment is Failed, AbortedByAlarm or RollingBack
//   - "Ready"    — the newest Bundle is Verified in every environment it reached
//
// Each environment is judged by its newest Bundle only (newest step creation
// time, ties broken by bundle name), and by every step that Bundle has there,
// so a multi-region environment is Degraded when any region is, whatever the
// List order.
func DerivePhase(steps []kardinalv1alpha1.PromotionStep) string {
	if len(steps) == 0 {
		return "Unknown"
	}

	// Pass 1: the newest Bundle per environment.
	type envKey struct{ pipeline, env string }
	type bundleAt struct {
		name    string
		created time.Time
	}
	newest := make(map[envKey]bundleAt)
	for i := range steps {
		s := &steps[i]
		key := envKey{s.Spec.PipelineName, s.Spec.Environment}
		at := bundleAt{s.Spec.BundleName, s.CreationTimestamp.Time}
		cur, ok := newest[key]
		if !ok || at.created.After(cur.created) ||
			(at.created.Equal(cur.created) && at.name > cur.name) {
			newest[key] = at
		}
	}

	// Pass 2: every step of that Bundle in the environment counts.
	hasDegraded := false
	allVerified := true
	for i := range steps {
		s := &steps[i]
		if newest[envKey{s.Spec.PipelineName, s.Spec.Environment}].name != s.Spec.BundleName {
			continue
		}
		if degradedStates[s.Status.State] {
			hasDegraded = true
		}
		if s.Status.State != "Verified" {
			allVerified = false
		}
	}

	switch {
	case hasDegraded:
		return "Degraded"
	case allVerified:
		return "Ready"
	default:
		return "Unknown"
	}
}

// validate checks pipeline invariants and returns the desired Ready condition:
// True/Valid, or False/ValidationFailed with the first problem found.
func (r *Reconciler) validate(p *kardinalv1alpha1.Pipeline) metav1.Condition {
	invalid := func(msg string) metav1.Condition {
		return metav1.Condition{
			Type: "Ready", Status: metav1.ConditionFalse, Reason: reasonValidationFailed,
			Message: msg, ObservedGeneration: p.Generation,
		}
	}

	// Check for duplicate environment names
	seen := make(map[string]struct{}, len(p.Spec.Environments))
	for _, env := range p.Spec.Environments {
		if _, exists := seen[env.Name]; exists {
			return invalid(fmt.Sprintf("duplicate environment name %q in pipeline spec", env.Name))
		}
		seen[env.Name] = struct{}{}
	}

	// Check dependsOn references valid environments
	for _, env := range p.Spec.Environments {
		for _, dep := range env.DependsOn {
			if _, exists := seen[dep]; !exists {
				return invalid(fmt.Sprintf(
					"environment %q has dependsOn %q which does not exist in this pipeline",
					env.Name, dep,
				))
			}
		}
	}

	// Check the promotion order resolves (no dependsOn or wave cycle). Every
	// Bundle of a cyclic pipeline would fail with CircularDependency.
	if err := graph.DetectCycle(p); err != nil {
		return invalid(err.Error())
	}

	return metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: reasonValid,
		Message: "Pipeline spec is valid", ObservedGeneration: p.Generation,
	}
}

// conditionMatches returns true if conditions already holds the desired Ready
// condition (status, reason, message and observed generation) — used for
// idempotency checks.
func conditionMatches(conditions []metav1.Condition, desired metav1.Condition) bool {
	c := meta.FindStatusCondition(conditions, desired.Type)
	return c != nil && c.Status == desired.Status && c.Reason == desired.Reason &&
		c.Message == desired.Message && c.ObservedGeneration == desired.ObservedGeneration
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
		// Deleting the freeze gate by hand while the pipeline is paused, or
		// removing a user gate that has its name, re-enqueues the Pipeline.
		Watches(&kardinalv1alpha1.PolicyGate{}, handler.EnqueueRequestsFromMapFunc(pipelineForFreezeGate)).
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
