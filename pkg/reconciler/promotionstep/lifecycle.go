// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// requeuePaused is the fallback requeue while a pipeline is paused. Resume
// normally wakes the step at once: deleting the freeze gate is a PolicyGate
// event, and SetupWithManager watches PolicyGates.
const requeuePaused = time.Minute

// autoRollbackActor is recorded as the author of onHealthFailure=rollback Bundles.
const autoRollbackActor = "kardinal-controller (onHealthFailure=rollback)"

// holdIfPaused keeps a step where it is while its pipeline is paused.
//
// It is called at the two safe points: before a Pending step starts, and
// before a Promoting step runs its next git step. Steps waiting for a PR merge
// or running a health check are not held, because stopping them would leave a
// merged change unverified.
//
// The signal is the freeze PolicyGate (lifecycle.IsPaused), not
// Pipeline.spec.paused: the Pipeline reconciler converges the gate to
// spec.paused, and the gate is a CRD the step can watch. The step writes only
// its own status message.
func (r *Reconciler) holdIfPaused(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (bool, ctrl.Result, error) {
	paused, err := lifecycle.IsPaused(ctx, r.Client, ps.Namespace, ps.Spec.PipelineName)
	if err != nil {
		return true, ctrl.Result{}, fmt.Errorf("check pause of pipeline %s: %w", ps.Spec.PipelineName, err)
	}
	if !paused {
		return false, ctrl.Result{}, nil
	}
	msg := lifecycle.PausedMessage(ps.Spec.PipelineName)
	if ps.Status.Message != msg {
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.Message = msg
		if err := r.Status().Patch(ctx, ps, patch); err != nil {
			return true, ctrl.Result{}, fmt.Errorf("patch paused message: %w", err)
		}
		log.Info().
			Str("pipeline", ps.Spec.PipelineName).
			Str("env", ps.Spec.Environment).
			Str("state", ps.Status.State).
			Msg("pipeline paused — step held")
	}
	return true, ctrl.Result{RequeueAfter: requeuePaused}, nil
}

// autoRollbackName is the name of the onHealthFailure=rollback Bundle of a
// failing Bundle. It is fixed so a retried reconcile, or a second region of
// the same environment, reuses the Bundle instead of creating another.
func autoRollbackName(bundle string) string {
	return bundle + "-rollback-alarm"
}

// createAutoRollback creates the onHealthFailure=rollback Bundle for a step
// whose health check failed. It uses lifecycle.PlanRollback, the same planner
// as `kardinal rollback` and the UI: the target is the most recent Bundle,
// other than the failing one, that was Verified in the environment, and the
// rollback Bundle copies its artifacts. The failing image is never
// re-promoted.
//
// It returns the rollback Bundle name. refusal is set, and nothing is
// created, when there is nothing safe to roll back to (no earlier Verified
// Bundle with different artifacts); the caller then stops the step for a
// human. err is a transient error to retry.
//
// Graph-first: it creates a new Bundle and writes no other object's status.
func (r *Reconciler) createAutoRollback(ctx context.Context, ps *v1alpha1.PromotionStep) (name string, refusal, err error) {
	name = autoRollbackName(ps.Spec.BundleName)
	var existing v1alpha1.Bundle
	getErr := r.Get(ctx, types.NamespacedName{Namespace: ps.Namespace, Name: name}, &existing)
	if getErr == nil {
		return name, nil, nil
	}
	if !apierrors.IsNotFound(getErr) {
		return "", nil, fmt.Errorf("get rollback bundle %s: %w", name, getErr)
	}

	plan, planErr := lifecycle.PlanRollback(ctx, r.Client, lifecycle.RollbackRequest{
		Namespace:   ps.Namespace,
		Pipeline:    ps.Spec.PipelineName,
		Environment: ps.Spec.Environment,
		FromBundle:  ps.Spec.BundleName,
		Actor:       autoRollbackActor,
		Name:        name,
		Reason:      "AutoRollback",
	})
	if planErr != nil {
		if errors.Is(planErr, lifecycle.ErrConflict) || errors.Is(planErr, lifecycle.ErrInvalid) ||
			errors.Is(planErr, lifecycle.ErrNotFound) {
			return "", planErr, nil
		}
		return "", nil, fmt.Errorf("plan rollback: %w", planErr)
	}
	if createErr := r.Create(ctx, plan.Bundle); createErr != nil && !apierrors.IsAlreadyExists(createErr) {
		return "", nil, fmt.Errorf("create rollback bundle %s: %w", name, createErr)
	}
	return name, nil, nil
}
