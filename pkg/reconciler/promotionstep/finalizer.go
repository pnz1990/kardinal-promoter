// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
)

// FinalizerClosePR keeps a PromotionStep that may hold an open PR until the
// controller has closed that PR. Without it, deleting a Bundle (or its
// namespace) deleted the steps and left their PRs open and mergeable; merging
// one later changed the environment with no PromotionStep tracking it.
//
// The finalizer is on a step only while it is Promoting or WaitingForMerge and
// opens, or has opened, a PR (spec.prStatusRef, or a PR URL in its status). It
// is added when such a step enters Promoting, before the PR is opened, so no
// PR exists without it. It is removed as soon as the step leaves those states
// (the PR was merged, closed, or the step ended), and on delete once the PR is
// closed.
//
// While the controller is not running, a deleted step that holds the
// finalizer stays until the controller is back. Uninstalling the controller
// before deleting the Bundles leaves such steps behind: remove the finalizer
// by hand (docs/troubleshooting.md).
const FinalizerClosePR = "kardinal.io/close-pr"

// closePRDeadline is how long after the delete request a step keeps retrying
// to close its PR. After that the finalizer is removed anyway, with a Warning
// Event, so a broken SCM cannot block a Bundle or namespace delete for good.
const closePRDeadline = 5 * time.Minute

// Bounds of the backoff between two attempts to close a deleted step's PR.
const (
	closePRMinDelay = 5 * time.Second
	closePRMaxDelay = time.Minute
)

// holdsPR reports whether a step in state can have an open PR it owns.
func holdsPR(state string) bool {
	return state == StatePromoting || state == StateWaitingForMerge
}

// needsPRFinalizer reports whether ps must hold FinalizerClosePR: it is in a
// state that can hold an open PR, and it is a step that opens one.
func needsPRFinalizer(ps *v1alpha1.PromotionStep) bool {
	if !holdsPR(ps.Status.State) {
		return false
	}
	return ps.Spec.PRStatusRef != "" || ps.Status.PRURL != "" || ps.Status.Outputs["prURL"] != ""
}

// syncPRFinalizer adds or removes FinalizerClosePR so that ps holds it exactly
// when needsPRFinalizer says so. It patches only metadata, with an optimistic
// lock, and only when something changes. It never runs on a deleting step.
func (r *Reconciler) syncPRFinalizer(ctx context.Context, ps *v1alpha1.PromotionStep) error {
	want := needsPRFinalizer(ps)
	if !ps.DeletionTimestamp.IsZero() || want == controllerutil.ContainsFinalizer(ps, FinalizerClosePR) {
		return nil
	}
	base := ps.DeepCopy()
	if want {
		controllerutil.AddFinalizer(ps, FinalizerClosePR)
	} else {
		controllerutil.RemoveFinalizer(ps, FinalizerClosePR)
	}
	if err := r.Patch(ctx, ps, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("patch finalizer %s (add=%v): %w", FinalizerClosePR, want, err)
	}
	return nil
}

// handleDeleted runs for a step with a deletionTimestamp. If the step holds
// FinalizerClosePR and may still own an open PR, it closes the PR with a
// comment (closeStepPR; a merged or closed PR is left alone) and then removes
// the finalizer. A failed close is retried with backoff until closePRDeadline
// after the delete request; then the finalizer is removed anyway and a Warning
// Event and an error log say the PR must be closed by hand.
func (r *Reconciler) handleDeleted(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(ps, FinalizerClosePR) {
		return ctrl.Result{}, nil
	}
	if holdsPR(ps.Status.State) {
		if err := r.closeStepPR(ctx, ps, r.deleteReason(ctx, ps)); err != nil {
			elapsed := time.Since(ps.DeletionTimestamp.Time)
			if elapsed < closePRDeadline {
				delay := closePRRetryDelay(elapsed)
				log.Warn().Err(err).Dur("retryIn", delay).
					Msg("closing the PR of a deleted PromotionStep failed; retrying")
				return ctrl.Result{RequeueAfter: delay}, nil
			}
			note := fmt.Sprintf("env %s: could not close the PR of the deleted step within %s (%v); "+
				"close it by hand: merging it would change the environment with no PromotionStep tracking it",
				ps.Spec.Environment, closePRDeadline, err)
			log.Error().Err(err).Str("env", ps.Spec.Environment).Str("prURL", ps.Status.PRURL).
				Msg("gave up closing the PR of a deleted PromotionStep; removing its finalizer")
			kubeevent.Emit(r.Recorder, ps, corev1.EventTypeWarning, "ClosePRFailed", "Delete", note)
		}
	}
	base := ps.DeepCopy()
	controllerutil.RemoveFinalizer(ps, FinalizerClosePR)
	if err := r.Patch(ctx, ps, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("remove finalizer %s: %w", FinalizerClosePR, err)
	}
	return ctrl.Result{}, nil
}

// deleteReason is the reason the PR comment gives for closing it: the Bundle
// was deleted (gone or being deleted, which includes its namespace being
// deleted), or else only the step was.
func (r *Reconciler) deleteReason(ctx context.Context, ps *v1alpha1.PromotionStep) string {
	if ps.Spec.BundleName == "" {
		return "PromotionStep " + ps.Name + " was deleted"
	}
	var b v1alpha1.Bundle
	err := r.Get(ctx, types.NamespacedName{Name: ps.Spec.BundleName, Namespace: ps.Namespace}, &b)
	if apierrors.IsNotFound(err) || (err == nil && !b.DeletionTimestamp.IsZero()) {
		return "bundle " + ps.Spec.BundleName + " was deleted"
	}
	return "PromotionStep " + ps.Name + " was deleted"
}

// closePRRetryDelay is the wait before the next attempt to close a deleted
// step's PR, elapsed after the delete request: half the time spent so far,
// within [closePRMinDelay, closePRMaxDelay], and never past closePRDeadline
// (so the final attempt happens at the deadline).
func closePRRetryDelay(elapsed time.Duration) time.Duration {
	d := min(max(elapsed/2, closePRMinDelay), closePRMaxDelay)
	if left := closePRDeadline - elapsed; d > left {
		d = max(left, time.Second)
	}
	return d
}
