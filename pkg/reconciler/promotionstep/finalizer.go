// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
)

// FinalizerClosePR keeps a PromotionStep that may hold an open PR until the
// controller has closed that PR. Without it, deleting a Bundle (or its
// namespace) deleted the steps and left their PRs open and mergeable; merging
// one later changed the environment with no PromotionStep tracking it.
//
// The finalizer is on a step only while it is Promoting or WaitingForMerge and
// opens, or has opened, a PR: its step sequence has open-pr (a pr-review
// environment), or its status has a PR URL. An auto step never holds it. It is
// added when such a step enters Promoting, before the PR is opened, so no PR
// exists without it. It is removed as soon as the step leaves those states
// (the PR was merged, closed, or the step ended), and on delete once the PR is
// closed.
//
// While the controller is not running, a deleted step that holds the
// finalizer stays until the controller is back. Uninstalling the controller
// before deleting the Bundles, or downgrading to a controller that does not
// know the finalizer, leaves such steps behind: remove the finalizer by hand
// (docs/installation.md, Uninstall and Downgrading).
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

// openPRStep is the name of the step that opens the PR, in status.steps.
const openPRStep = "open-pr"

// needsPRFinalizer reports whether ps must hold FinalizerClosePR: it is in a
// state that can hold an open PR, and it is a step that opens one. That is
// read from the step sequence handlePending writes on entering Promoting
// (only pr-review environments have open-pr), or from a PR URL in the status.
// spec.prStatusRef says nothing: the Graph builder sets it on every step.
func needsPRFinalizer(ps *v1alpha1.PromotionStep) bool {
	if !holdsPR(ps.Status.State) {
		return false
	}
	opensPR := slices.ContainsFunc(ps.Status.Steps, func(s v1alpha1.StepStatus) bool { return s.Name == openPRStep })
	return opensPR || ps.Status.PRURL != "" || ps.Status.Outputs["prURL"] != ""
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

// requeuePRFinalizerConflict is how soon a step is reconciled again when
// syncing FinalizerClosePR hit a conflict.
const requeuePRFinalizerConflict = time.Second

// prFinalizerSyncFailed is the result of a reconcile whose syncPRFinalizer
// failed. A conflict means kro or the controller wrote the step since it was
// read: the step is reconciled again shortly and syncs the finalizer against
// the new copy, so the conflict is not a reconcile error. Either way the
// reconcile stops, so a step that needs the finalizer never reaches its state
// handler (which opens the PR) without it.
func prFinalizerSyncFailed(log zerolog.Logger, err error) (ctrl.Result, error) {
	if apierrors.IsConflict(err) {
		log.Debug().Err(err).Msg("step changed while syncing its close-pr finalizer; trying again")
		return ctrl.Result{RequeueAfter: requeuePRFinalizerConflict}, nil
	}
	return ctrl.Result{}, err
}

// handleDeleted runs for a step with a deletionTimestamp. If the step holds
// FinalizerClosePR and may still own an open PR, it closes the PR with a
// comment (closeStepPR; a merged or closed PR is left alone) and then removes
// the finalizer. A failed close is retried with backoff until closePRDeadline
// after the delete request; then the finalizer is removed anyway and a Warning
// Event and an error log say the PR must be closed by hand.
//
// The PR is left open when a new step reuses it (stepRecreated): the step went
// with its Graph while the Bundle goes on promoting, so the Bundle reconciler
// recreates the Graph and the new step reuses the PR. Closing it there made
// the new step open a second PR. If no new step comes (the Bundle is deleted
// or stops promoting first), nothing closes that PR: docs/troubleshooting.md
// says how to find it. A failed read in stepRecreated is retried like a failed
// close; past closePRDeadline the PR is closed.
//
// In a namespace being deleted the API server refuses the ClosePRFailed
// Event, so the error log is the only record of a PR left open there.
//
// The step is read again from the API server first: the cached step can still
// hold the finalizer the previous reconcile removed, and closing from it
// closed and commented on the PR a second time.
func (r *Reconciler) handleDeleted(ctx context.Context, log zerolog.Logger, cached *v1alpha1.PromotionStep) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cached, FinalizerClosePR) {
		return ctrl.Result{}, nil
	}
	ps, err := r.readStep(ctx, client.ObjectKeyFromObject(cached))
	if err != nil || ps == nil || !controllerutil.ContainsFinalizer(ps, FinalizerClosePR) {
		return ctrl.Result{}, err
	}
	if !holdsPR(ps.Status.State) {
		// Past its PR: there is nothing to close.
		return ctrl.Result{}, r.removePRFinalizer(ctx, ps)
	}
	elapsed := r.now().Sub(ps.DeletionTimestamp.Time)
	recreated, err := r.stepRecreated(ctx, ps)
	switch {
	case err != nil && elapsed < closePRDeadline:
		// Closing the PR of a step that comes back would make the new step
		// open a second one, so a failed read is retried, not taken as "no".
		delay := closePRRetryDelay(elapsed)
		log.Warn().Err(err).Dur("retryIn", delay).
			Msg("could not tell whether a deleted PromotionStep comes back; retrying before closing its PR")
		return ctrl.Result{RequeueAfter: delay}, nil
	case err == nil && recreated:
		log.Info().Str("env", ps.Spec.Environment).Str("prURL", ps.Status.PRURL).
			Msg("left the PR of a step deleted with its Graph open: the Bundle recreates the Graph, " +
				"and the new step reuses the PR")
	default:
		// Past the deadline a read that still fails counts as "does not come
		// back": a second PR is better than an open one nothing tracks.
		if err != nil {
			log.Warn().Err(err).Msg("still cannot tell whether a deleted PromotionStep comes back; closing its PR")
		}
		if err := r.closeStepPR(ctx, ps, r.deleteReason(ctx, ps)); err != nil {
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
	return ctrl.Result{}, r.removePRFinalizer(ctx, ps)
}

// stepRecreated reports whether a new step for ps's environment will reuse
// ps's open PR. That is when ps went with its Graph (deleted by hand, for
// instance) while the Bundle goes on promoting: the Bundle reconciler
// recreates the Graph, and kro creates a new step for ps's environment, which
// finds and reuses ps's open PR (GRAPH-HEAL-01). All of these must hold, read
// from the API server:
//
//   - the Bundle exists, is not being deleted, and is Promoting;
//   - its namespace is not being deleted;
//   - its Pipeline exists and still has ps's environment;
//   - its Graph is gone, is being deleted, or was created at or after ps's
//     delete request (it was recreated before ps was reconciled).
//
// A step deleted on its own is not one: kro applies it again under the same
// name, but only once it is gone, so after handleDeleted closed its PR, and
// the new step opens a new PR (checked on kind). A step whose environment a
// Pipeline edit dropped (kro updates the Graph in place) does not come back,
// nor does one whose Bundle or namespace is being deleted or gone. A read that
// fails for another reason than NotFound is an error: handleDeleted retries it
// rather than close a PR the new step would reuse.
func (r *Reconciler) stepRecreated(ctx context.Context, ps *v1alpha1.PromotionStep) (bool, error) {
	if ps.Spec.BundleName == "" {
		return false, nil
	}
	reader := r.apiReader()
	key := func(name string) client.ObjectKey { return client.ObjectKey{Namespace: ps.Namespace, Name: name} }
	// read reports whether obj exists; an error other than NotFound fails.
	read := func(k client.ObjectKey, obj client.Object, what string) (bool, error) {
		err := reader.Get(ctx, k, obj)
		switch {
		case err == nil:
			return true, nil
		case apierrors.IsNotFound(err):
			return false, nil
		default:
			return false, fmt.Errorf("get %s %s: %w", what, k.Name, err)
		}
	}
	var b v1alpha1.Bundle
	if ok, err := read(key(ps.Spec.BundleName), &b, "bundle"); !ok || err != nil {
		return false, err
	}
	if !b.DeletionTimestamp.IsZero() || b.Status.Phase != bundlePhasePromoting {
		return false, nil
	}
	var ns corev1.Namespace
	if ok, err := read(client.ObjectKey{Name: ps.Namespace}, &ns, "namespace"); !ok || err != nil {
		return false, err
	}
	if !ns.DeletionTimestamp.IsZero() || ns.Status.Phase == corev1.NamespaceTerminating {
		return false, nil
	}
	var pl v1alpha1.Pipeline
	if ok, err := read(key(b.Spec.Pipeline), &pl, "pipeline"); !ok || err != nil {
		return false, err
	}
	if !slices.ContainsFunc(pl.Spec.Environments, func(e v1alpha1.EnvironmentSpec) bool {
		return e.Name == ps.Spec.Environment
	}) {
		return false, nil
	}
	name := b.Status.GraphRef
	if name == "" {
		name = graph.GraphNameFrom(b.Spec.Pipeline, b.Name)
	}
	g := &unstructured.Unstructured{}
	g.SetGroupVersionKind(graph.GraphGVK)
	if ok, err := read(key(name), g, "graph"); !ok || err != nil {
		return err == nil, err // a Graph that is gone is recreated
	}
	created := g.GetCreationTimestamp()
	return g.GetDeletionTimestamp() != nil || !created.Before(ps.DeletionTimestamp), nil
}

// bundlePhasePromoting is the phase of a Bundle whose Graph is promoting it.
const bundlePhasePromoting = "Promoting"

// removePRFinalizer removes FinalizerClosePR from the deleted step ps. A
// conflict is retried at once against the step read again from the API server,
// rather than by a new reconcile, which would close the PR again.
func (r *Reconciler) removePRFinalizer(ctx context.Context, ps *v1alpha1.PromotionStep) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		base := ps.DeepCopy()
		controllerutil.RemoveFinalizer(ps, FinalizerClosePR)
		err := r.Patch(ctx, ps, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		if !apierrors.IsConflict(err) {
			return client.IgnoreNotFound(err)
		}
		fresh, rerr := r.readStep(ctx, client.ObjectKeyFromObject(ps))
		if rerr != nil {
			return rerr
		}
		if fresh == nil || !controllerutil.ContainsFinalizer(fresh, FinalizerClosePR) {
			return nil
		}
		*ps = *fresh
		return err
	})
	if err != nil {
		return fmt.Errorf("remove finalizer %s: %w", FinalizerClosePR, err)
	}
	return nil
}

// readStep reads the step key from the API server (APIReader, or Client when
// it is nil). A step that is gone is nil.
func (r *Reconciler) readStep(ctx context.Context, key client.ObjectKey) (*v1alpha1.PromotionStep, error) {
	var ps v1alpha1.PromotionStep
	if err := r.apiReader().Get(ctx, key, &ps); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get promotionstep %s: %w", key, err)
	}
	return &ps, nil
}

// apiReader is APIReader, or Client when it is nil.
func (r *Reconciler) apiReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
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
