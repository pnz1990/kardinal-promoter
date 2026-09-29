// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package changewindow implements the ChangeWindow reconciler and the pure
// Evaluate function that decides whether a window is active.
//
// Architecture context (Graph-first question 2, Owned node):
//   - The reconciler writes only its own CRD status (status.active, status.reason
//     and the Valid condition).
//   - It calls time.Now() only to compute that status write.
//   - It requeues at the next boundary (blackout start/end, or the start/end of
//     a recurring window's allowed hours), so the status change is a watch event
//     that makes the PolicyGate reconciler re-evaluate gates that use the window.
//
// The PolicyGate reconciler calls the same Evaluate with its own evaluation
// time, so a gate evaluated a moment after a boundary never uses a stale status.
package changewindow

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/eventfilter"
)

// maxRequeue bounds the wait between evaluations, as a safety net for clock
// or timezone-database changes. Spec edits trigger a reconcile on their own.
const maxRequeue = time.Hour

// boundarySlack makes the requeue land just after a boundary, not just before it.
const boundarySlack = time.Second

// Reconciler writes ChangeWindow status.active and status.reason.
// It is idempotent and safe to re-run after a crash.
type Reconciler struct {
	client.Client
	// NowFn returns the current time. Overridable for testing.
	NowFn func() time.Time
}

// Reconcile evaluates one ChangeWindow and writes its status when it changed.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().Str("changewindow", req.Name).Logger()

	var cw kardinalv1alpha1.ChangeWindow
	if err := r.Get(ctx, req.NamespacedName, &cw); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get changewindow: %w", err)
	}

	now := r.now()
	res := Evaluate(cw.Spec, now)
	if res.Err != nil {
		log.Warn().Err(res.Err).Msg("invalid ChangeWindow; reported as active (blocking)")
	}

	patch := client.MergeFrom(cw.DeepCopy())
	changed := cw.Status.Active != res.Active || cw.Status.Reason != res.Reason
	cw.Status.Active = res.Active
	cw.Status.Reason = res.Reason
	if meta.SetStatusCondition(&cw.Status.Conditions, validCondition(res, cw.Generation, now)) {
		changed = true
	}
	if changed {
		if err := r.Status().Patch(ctx, &cw, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch changewindow status: %w", err)
		}
		log.Info().Bool("active", res.Active).Str("reason", res.Reason).Msg("changewindow status updated")
	}

	if res.Next.IsZero() {
		return ctrl.Result{}, nil
	}
	wait := res.Next.Sub(now) + boundarySlack
	if wait > maxRequeue {
		wait = maxRequeue
	}
	return ctrl.Result{RequeueAfter: wait}, nil
}

// SetupWithManager registers the reconciler with the manager. Only spec or
// annotation changes trigger a reconcile; the reconciler's own status write does
// not, and boundaries are driven by RequeueAfter.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.ChangeWindow{}, builder.WithPredicates(eventfilter.SpecOrAnnotationChanged)).
		Complete(r)
}

// ConditionValid is the ChangeWindow condition that reports whether the spec
// can be evaluated.
const ConditionValid = "Valid"

// validCondition is Valid=True, or Valid=False with the spec error, for
// example an unknown timezone. An invalid window is active (blocking), so the
// condition is what tells the operator why every gate that uses it blocks.
func validCondition(res Result, generation int64, now time.Time) metav1.Condition {
	cond := metav1.Condition{
		Type:               ConditionValid,
		Status:             metav1.ConditionTrue,
		Reason:             "SpecValid",
		Message:            "the spec is valid",
		ObservedGeneration: generation,
		LastTransitionTime: metav1.NewTime(now),
	}
	if res.Err != nil {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "InvalidSpec"
		cond.Message = res.Err.Error() + "; the window is active (blocking) until the spec is fixed"
	}
	return cond
}

func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now().UTC()
}
