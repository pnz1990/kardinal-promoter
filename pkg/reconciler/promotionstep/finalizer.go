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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// FinalizerClosePR keeps a PromotionStep that may hold an open PR until the
// controller has closed that PR. Without it, deleting a Bundle (or its
// namespace) deleted the steps and left their PRs open and mergeable; merging
// one later changed the environment with no PromotionStep tracking it.
//
// The finalizer is on a step only while it is Promoting or WaitingForMerge and
// opens, or has opened, a PR: its recorded step sequence has open-pr (the
// environment was pr-review when the step started), or its status has a PR
// URL. An auto step never holds it. It is added when such a step enters
// Promoting, before the PR is opened, so no PR exists without it; the step
// runs that recorded sequence, so an approval edit cannot open a PR the
// finalizer was not added for. It is removed as soon as the step leaves those
// states (the PR was merged, closed, or the step ended), and on delete once
// the PR is closed.
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
const openPRStep = steps.OpenPRStepName

// opensPR reports whether the recorded sequence of ps opens a PR (only
// environments that were pr-review when the step started have open-pr).
func opensPR(ps *v1alpha1.PromotionStep) bool {
	return slices.ContainsFunc(ps.Status.Steps, func(s v1alpha1.StepStatus) bool { return s.Name == openPRStep })
}

// needsPRFinalizer reports whether ps must hold FinalizerClosePR: it is in a
// state that can hold an open PR, and it is a step that opens one. That is
// read from the recorded step sequence, or from a PR URL in the status.
// spec.prStatusRef says nothing: the Graph builder sets it on every step.
func needsPRFinalizer(ps *v1alpha1.PromotionStep) bool {
	if !holdsPR(ps.Status.State) {
		return false
	}
	return opensPR(ps) || ps.Status.PRURL != "" || ps.Status.Outputs["prURL"] != ""
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
// comment and deletes its head branch (closeStepPR; a merged PR is left
// alone, and a closed one only loses its branch) and then removes the
// finalizer. A failed close or delete is retried with backoff until
// closePRDeadline after the delete request; then the finalizer is removed
// anyway and a Warning Event and an error log say what to close or delete by
// hand.
//
// The PR is left open when a new step reuses it (comebackReusesPR): the step
// went with its Graph while the Bundle goes on promoting, so the Bundle
// reconciler recreates the Graph and the new step reuses the PR. Closing it
// there made the new step open a second PR. If no new step comes (the Bundle
// is deleted or stops promoting first), nothing closes that PR:
// docs/troubleshooting.md says how to find it. A failed read in stepComeback
// is retried like a failed close. Past closePRDeadline the finalizer is
// removed and the PR is left open and uncommented, with an error log and a
// PRLeftOpen Warning Event: leaking an open PR is safer than closing one a
// recreated step may own.
//
// The PR is closed but its head branch kept when a new step pushes that
// branch again at once (comebackPushesBranch): the step was deleted on its
// own, and kro applies it again as soon as the finalizer is gone. Forgejo and
// Gitea delete a branch at once but close its open PRs later, from a queue,
// and they close every open PR of that branch name, so the new step's PR,
// opened about a second later, was closed too (B79). The new step force-pushes
// the branch with the same Bundle's change, so a late merge of the closed PR
// through GitHub's API delivers what the new step promotes, untracked. When
// the new step would wait (a gate is not ready, the Pipeline is paused, an
// upstream is not Verified) or never come (kro rejected the Graph), the branch
// is deleted: nothing else would delete it. A step with no PR yet gets the same
// choice for the branch it may have pushed (closeStepPR).
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
	back, err := r.stepComeback(ctx, ps)
	switch {
	case err != nil && elapsed < closePRDeadline:
		// Closing the PR of a step that comes back would make the new step
		// open a second one, so a failed read is retried, not taken as "no".
		delay := closePRRetryDelay(elapsed)
		log.Warn().Err(err).Dur("retryIn", delay).
			Msg("could not tell whether a deleted PromotionStep comes back; retrying before closing its PR")
		return ctrl.Result{RequeueAfter: delay}, nil
	case err != nil:
		// Past the deadline the step may still come back and reuse the PR, and
		// closing it would close the new step's PR under it. An open PR nothing
		// tracks can be found and closed by hand (docs/troubleshooting.md).
		note := fmt.Sprintf("env %s: could not tell within %s whether the deleted step comes back (%v); "+
			"left its PR open: close it by hand if no new PromotionStep uses it", ps.Spec.Environment, closePRDeadline, err)
		log.Error().Err(err).Str("env", ps.Spec.Environment).Str("prURL", ps.Status.PRURL).
			Msg("gave up telling whether a deleted PromotionStep comes back; left its PR open and removed its finalizer")
		kubeevent.Emit(r.Recorder, ps, corev1.EventTypeWarning, ReasonPRLeftOpen, "Delete", note)
	case back == comebackReusesPR:
		log.Info().Str("env", ps.Spec.Environment).Str("prURL", ps.Status.PRURL).
			Msg("left the PR of a step deleted with its Graph open: the Bundle recreates the Graph, " +
				"and the new step reuses the PR")
	default:
		keepBranch := back == comebackPushesBranch
		if err := r.closeStepPR(ctx, ps, r.deleteReason(ctx, ps), keepBranch); err != nil {
			if elapsed < closePRDeadline {
				delay := closePRRetryDelay(elapsed)
				log.Warn().Err(err).Dur("retryIn", delay).
					Msg("closing the PR of a deleted PromotionStep failed; retrying")
				return ctrl.Result{RequeueAfter: delay}, nil
			}
			note := fmt.Sprintf("env %s: could not close the PR of the deleted step within %s (%v); "+
				"%s: merging it would change the environment with no PromotionStep tracking it",
				ps.Spec.Environment, closePRDeadline, err, closeByHand(err))
			log.Error().Err(err).Str("env", ps.Spec.Environment).Str("prURL", ps.Status.PRURL).
				Msg("gave up closing the PR of a deleted PromotionStep; removing its finalizer")
			kubeevent.Emit(r.Recorder, ps, corev1.EventTypeWarning, "ClosePRFailed", "Delete", note)
		}
	}
	return ctrl.Result{}, r.removePRFinalizer(ctx, ps)
}

// ReasonPRLeftOpen is the Warning Event reason for a deleted step whose PR was
// left open because the controller could not tell whether the step comes back.
const ReasonPRLeftOpen = "PRLeftOpen"

// comeback is whether, and how, a new step for a deleted step's environment
// comes after it.
type comeback int

const (
	// noComeback: no new step pushes the deleted step's branch again.
	noComeback comeback = iota
	// comebackReusesPR: the step went with its Graph; a new step finds and
	// reuses its open PR.
	comebackReusesPR
	// comebackPushesBranch: the step was deleted on its own; kro applies it
	// again once it is gone, and the new step pushes the same branch at once
	// and opens a new PR (newStepPushesAtOnce).
	comebackPushesBranch
)

// stepComeback reports whether a new step comes for ps's environment, read
// from the API server. A new step reuses ps's open PR (comebackReusesPR) when
// ps went with its Graph (deleted by hand, for instance) while the Bundle goes
// on promoting: the Bundle reconciler recreates the Graph, and kro creates a
// new step for ps's environment, which finds and reuses ps's open PR
// (GRAPH-HEAL-01). All of these must hold:
//
//   - the Bundle exists, is not being deleted, and is Promoting;
//   - its namespace is not being deleted;
//   - its Pipeline exists and still has ps's environment;
//   - its Graph is gone, is being deleted, or was created at or after ps's
//     delete request (it was recreated before ps was reconciled).
//
// A step deleted on its own (comebackPushesBranch) does not reuse the PR: kro
// applies it again under the same name, but only once it is gone, so after
// handleDeleted closed its PR, and the new step pushes the same branch and
// opens a new PR (checked on kind). That is when the Bundle is Promoting or
// Failed (a Failed Bundle's new step runs again), the rest holds, the Graph is
// there and not being deleted, and the new step pushes at once
// (newStepPushesAtOnce). A Promoting Bundle's Graph must be older than the
// delete request (a newer one was recreated, so the new step reuses the PR);
// a Failed Bundle's Graph can be of any age, since nothing recreates it. A
// superseded Bundle's new step is cancelled before it pushes.
//
// A step whose environment a Pipeline edit dropped (kro updates the Graph in
// place) does not come back, nor does one whose Bundle or namespace is being
// deleted or gone, nor one of a Failed Bundle whose Graph went (it is not
// recreated). Nor, for this purpose, does a step kro would not apply again at
// once, or that would not push at once: its PR is closed and its branch
// deleted, so the branch is not left with no PR. A read that fails for
// another reason than NotFound is an error: handleDeleted retries it rather
// than close a PR the new step would reuse.
func (r *Reconciler) stepComeback(ctx context.Context, ps *v1alpha1.PromotionStep) (comeback, error) {
	if ps.Spec.BundleName == "" {
		return noComeback, nil
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
		return noComeback, err
	}
	promoting := b.Status.Phase == bundlePhasePromoting
	if !b.DeletionTimestamp.IsZero() || (!promoting && b.Status.Phase != bundlePhaseFailed) {
		return noComeback, nil
	}
	var ns corev1.Namespace
	if ok, err := read(client.ObjectKey{Name: ps.Namespace}, &ns, "namespace"); !ok || err != nil {
		return noComeback, err
	}
	if !ns.DeletionTimestamp.IsZero() || ns.Status.Phase == corev1.NamespaceTerminating {
		return noComeback, nil
	}
	var pl v1alpha1.Pipeline
	if ok, err := read(key(b.Spec.Pipeline), &pl, "pipeline"); !ok || err != nil {
		return noComeback, err
	}
	if !slices.ContainsFunc(pl.Spec.Environments, func(e v1alpha1.EnvironmentSpec) bool {
		return e.Name == ps.Spec.Environment
	}) {
		return noComeback, nil
	}
	name := b.Status.GraphRef
	if name == "" {
		name = graph.GraphNameFrom(b.Spec.Pipeline, b.Name)
	}
	g := &unstructured.Unstructured{}
	g.SetGroupVersionKind(graph.GraphGVK)
	ok, err := read(key(name), g, "graph")
	if err != nil {
		return noComeback, err
	}
	created := g.GetCreationTimestamp()
	switch {
	case !ok || g.GetDeletionTimestamp() != nil:
		if promoting {
			return comebackReusesPR, nil // the Bundle reconciler recreates the Graph
		}
		return noComeback, nil // nothing recreates a Failed Bundle's Graph
	case promoting && !created.Before(ps.DeletionTimestamp):
		return comebackReusesPR, nil // the Graph was recreated before ps was reconciled
	}
	// The Graph is there, so kro applies the step again once ps is gone.
	pushes, err := newStepPushesAtOnce(ctx, reader, ps, &b, &pl, g, r.AllowedRepositories)
	if err != nil || !pushes {
		return noComeback, err
	}
	return comebackPushesBranch, nil
}

// newStepPushesAtOnce reports whether the step kro applies again for ps, once
// ps is gone, pushes ps's branch at once. Keeping the branch is safe only
// then: a new step that waits, or never comes, never deletes it, and the
// branch stayed with no PR. kro creates the new step only while the Graph is
// accepted, each upstream environment has the Bundle's step Verified, and
// each required gate is ready (the Graph builder's resolvableWhen). The new
// step then waits while the Pipeline is paused (holdIfPaused), and fails at
// once on a configuration it does not support (unsupportedConfig). A required
// gate that is gone counts as not ready. A read that fails is an error.
//
// It is a prediction: a gate or a pause that changes in the second before kro
// applies the step again can still make the new step wait with the kept
// branch. docs/troubleshooting.md says how to find such a branch.
func newStepPushesAtOnce(ctx context.Context, reader client.Reader, ps *v1alpha1.PromotionStep,
	b *v1alpha1.Bundle, pl *v1alpha1.Pipeline, g *unstructured.Unstructured, allowed *scm.RepositoryAllowlist) (bool, error) {
	if unsupportedConfig(pl, findEnv(pl, ps.Spec.Environment), ps, allowed) != "" {
		return false, nil
	}
	rejected, err := graphRejected(g, b)
	if err != nil || rejected {
		return false, err
	}
	for _, name := range ps.Spec.RequiredGates {
		var gate v1alpha1.PolicyGate
		if err := reader.Get(ctx, client.ObjectKey{Namespace: ps.Namespace, Name: name}, &gate); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, fmt.Errorf("get policygate %s: %w", name, err)
		}
		if !gate.Status.Ready {
			return false, nil
		}
	}
	paused, err := lifecycle.IsPaused(ctx, reader, ps.Namespace, pl.Name)
	if err != nil || paused {
		return false, err
	}
	var list v1alpha1.PromotionStepList
	if err := reader.List(ctx, &list, client.InNamespace(ps.Namespace),
		client.MatchingLabels{"kardinal.io/bundle": b.Name}); err != nil {
		return false, fmt.Errorf("list promotionsteps of bundle %s: %w", b.Name, err)
	}
	// A step being deleted is not one kro reads: it applies it again too.
	live := slices.DeleteFunc(list.Items, func(s v1alpha1.PromotionStep) bool { return !s.DeletionTimestamp.IsZero() })
	return graph.UpstreamsVerified(pl, b, ps.Spec.Environment, live), nil
}

// graphRejected reports whether kro rejected the Graph g and so applies none
// of its nodes: its Accepted condition for the current generation is False.
// When kro has not judged the current generation yet, the Bundle's copy of
// that condition (GraphAccepted, which the Bundle reconciler keeps) decides.
func graphRejected(g *unstructured.Unstructured, b *v1alpha1.Bundle) (bool, error) {
	typed, err := graph.FromUnstructured(g)
	if err != nil {
		return false, fmt.Errorf("read graph %s: %w", g.GetName(), err)
	}
	if c := meta.FindStatusCondition(typed.Status.Conditions, "Accepted"); c != nil &&
		(c.ObservedGeneration == 0 || c.ObservedGeneration >= typed.Generation) {
		return c.Status == metav1.ConditionFalse, nil
	}
	c := meta.FindStatusCondition(b.Status.Conditions, "GraphAccepted")
	return c != nil && c.Status == metav1.ConditionFalse, nil
}

// Phases of a Bundle whose deleted step kro applies again.
const (
	bundlePhasePromoting = "Promoting"
	bundlePhaseFailed    = "Failed"
)

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

// deleteReason is the reason the PR comment gives for closing it: the
// namespace was deleted (it is being deleted), else the Bundle was (gone or
// being deleted), else only the step was. The namespace comes first: its
// deletion deletes the Bundle and the steps in no set order, and the comment
// must not depend on which went first. The namespace is read from the API
// server, which the controller may only get, not watch.
func (r *Reconciler) deleteReason(ctx context.Context, ps *v1alpha1.PromotionStep) string {
	var ns corev1.Namespace
	if err := r.apiReader().Get(ctx, client.ObjectKey{Name: ps.Namespace}, &ns); err == nil &&
		(!ns.DeletionTimestamp.IsZero() || ns.Status.Phase == corev1.NamespaceTerminating) {
		return "namespace " + ps.Namespace + " was deleted"
	}
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
