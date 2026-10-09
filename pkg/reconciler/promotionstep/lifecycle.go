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
	"k8s.io/apimachinery/pkg/api/meta"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// requeuePaused is how often a held step re-checks its pipeline's freeze
// gate. Resume takes effect at the next check, up to a minute later: the
// PolicyGate watch (policyGateMapper) wakes only the steps whose
// spec.requiredGates names the gate, and the freeze gate is not listed there.
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

// holdForSlot keeps a Pending step Pending while its Bundle waits for a
// maxConcurrentPromotions slot (#1349): the Bundle is Failed, and the Bundle
// reconciler set its graph.CondBundleWaitingForSlot condition because other
// Bundles of the Pipeline fill the cap. The Graph creates no new step of
// such a Bundle (spec.bundleName does not resolve); this holds the steps that
// already exist, before any git operation. Lifting the hold wakes the step
// (bundleWakesSteps); requeueSlotHold is the fallback. The step writes only
// its own status message.
func (r *Reconciler) holdForSlot(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (bool, ctrl.Result, error) {
	var b v1alpha1.Bundle
	if err := r.Get(ctx, client.ObjectKey{Name: ps.Spec.BundleName, Namespace: ps.Namespace}, &b); err != nil {
		if apierrors.IsNotFound(err) {
			return false, ctrl.Result{}, nil // the orphan guard deletes the step
		}
		return true, ctrl.Result{}, fmt.Errorf("load bundle %s for the slot hold: %w", ps.Spec.BundleName, err)
	}
	if !slotHeld(&b) {
		return false, ctrl.Result{}, nil
	}
	msg := fmt.Sprintf("held: Bundle %s waits for a maxConcurrentPromotions slot of pipeline %s",
		ps.Spec.BundleName, ps.Spec.PipelineName)
	if ps.Status.Message != msg {
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.Message = msg
		if err := r.Status().Patch(ctx, ps, patch); err != nil {
			if apierrors.IsNotFound(err) {
				return true, ctrl.Result{}, nil
			}
			return true, ctrl.Result{}, fmt.Errorf("patch slot hold message: %w", err)
		}
		log.Info().Str("bundle", ps.Spec.BundleName).Str("env", ps.Spec.Environment).
			Msg("bundle waits for a maxConcurrentPromotions slot — step held")
	}
	return true, ctrl.Result{RequeueAfter: requeueSlotHold}, nil
}

// requeueSlotHold is the fallback re-check of a step held by holdForSlot.
const requeueSlotHold = 30 * time.Second

// slotHeld reports whether b waits for a maxConcurrentPromotions slot.
func slotHeld(b *v1alpha1.Bundle) bool {
	return meta.IsStatusConditionTrue(b.Status.Conditions, graph.CondBundleWaitingForSlot)
}

// bundleWakesSteps passes the Bundle events that steps act on: a Bundle that
// is Superseded or Rejected (the supersession guard, isHalted) and a slot
// hold set or lifted.
var bundleWakesSteps = predicate.Funcs{
	CreateFunc:  func(e event.CreateEvent) bool { return isHalted(e.Object) },
	DeleteFunc:  func(e event.DeleteEvent) bool { return isHalted(e.Object) },
	GenericFunc: func(e event.GenericEvent) bool { return isHalted(e.Object) },
	UpdateFunc: func(e event.UpdateEvent) bool {
		if isHalted(e.ObjectNew) {
			return true
		}
		oldB, okOld := e.ObjectOld.(*v1alpha1.Bundle)
		newB, okNew := e.ObjectNew.(*v1alpha1.Bundle)
		return okOld && okNew && slotHeld(oldB) != slotHeld(newB)
	},
}

// createAutoRollback creates the onHealthFailure=rollback Bundle for a step
// whose health check failed. It uses lifecycle.PlanRollback, the same planner
// as `kardinal rollback` and the UI: the target is the most recent Bundle,
// other than the failing one, that was Verified in the environment, and the
// rollback Bundle copies its artifacts. The failing image is never
// re-promoted.
//
// The name is fixed (lifecycle.AutoRollbackName), and an existing rollback of
// the failing Bundle in the environment, from this path or a RollbackPolicy,
// is reused (lifecycle.FindRollback), so a retried reconcile or a second
// region of the environment does not create another.
//
// It returns the rollback Bundle name. refusal is set, and nothing is
// created, when there is nothing safe to roll back to (no earlier Verified
// Bundle with different artifacts) or the failing Bundle is itself a
// rollback (a failing rollback does not start another); the caller then
// stops the step for a human. err is a transient error to retry.
//
// Graph-first: it creates a new Bundle and writes no other object's status.
func (r *Reconciler) createAutoRollback(ctx context.Context, ps *v1alpha1.PromotionStep) (name string, refusal, err error) {
	existing, findErr := lifecycle.FindRollback(ctx, r.Client, ps.Namespace,
		ps.Spec.PipelineName, ps.Spec.Environment, ps.Spec.BundleName)
	if findErr != nil {
		return "", nil, fmt.Errorf("find rollback of bundle %s: %w", ps.Spec.BundleName, findErr)
	}
	if existing != "" {
		return existing, nil, nil
	}
	name = lifecycle.AutoRollbackName(ps.Spec.BundleName, "alarm")

	plan, planErr := lifecycle.PlanRollback(ctx, r.Client, lifecycle.RollbackRequest{
		Namespace:   ps.Namespace,
		Pipeline:    ps.Spec.PipelineName,
		Environment: ps.Spec.Environment,
		FromBundle:  ps.Spec.BundleName,
		Actor:       autoRollbackActor,
		Name:        name,
		Reason:      "AutoRollback",
		Automatic:   true,
		// time.Now() here stamps the created-at annotation of the Bundle this
		// reconciler creates: a CRD write, like the other Bundle creators.
		Now: time.Now().UTC(),
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
