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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/eventfilter"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/shard"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
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
	// Recorder emits the Warning Event for an invalid spec. Nil records none.
	Recorder events.EventRecorder
	// NowFn returns the current time. Overridable for testing.
	NowFn func() time.Time
	// MaxExemption bounds how long a freeze exemption counts from when the
	// reconciler first saw it (--freeze-exemption-max-minutes). Zero is
	// DefaultMaxExemption.
	MaxExemption time.Duration
	// AuditNamespace is where the AuditEvents of a freeze of several or
	// every namespace go (the controller's namespace); a freeze of one
	// namespace is audited there.
	AuditNamespace string
}

// Reconcile evaluates one ChangeWindow and writes its status when it changed.
//
// A ChangeWindow deleted while it is reconciled ends the reconcile (objectgone).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return objectgone.Reconcile(ctx, req, changeWindowsResource, r.reconcile)
}

// changeWindowsResource is the resource objectgone matches a NotFound against.
var changeWindowsResource = kardinalv1alpha1.GroupVersion.WithResource("changewindows").GroupResource()

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().Str("changewindow", req.Name).Logger()

	var cw kardinalv1alpha1.ChangeWindow
	if err := r.Get(ctx, req.NamespacedName, &cw); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get changewindow: %w", err)
	}
	// A freeze's start and lift are audited, with who (#1939).
	if deleting, err := r.reconcileFreezeAudit(ctx, &cw); err != nil || deleting {
		return ctrl.Result{}, err
	}

	now := r.now()
	res := Evaluate(cw.Spec, now)
	// The invalid spec is reported once per generation: the Valid condition
	// records the generation it was found invalid at, so a boundary requeue, a
	// resync, an annotation edit or a controller restart reports nothing new.
	newlyInvalid := res.Err != nil && !invalidAt(cw, cw.Generation)

	// A freeze also records the exemptions in effect now, and the next
	// boundary includes the earliest of their expiries, so the status is
	// all a PromotionStep reads (no clock outside this status write).
	ex := freezeExemptions(cw.Spec.Freeze, cw.Status.ExemptionRecords, now, r.maxExemption())
	exemptions, next := ex.inEffect, res.Next
	for _, u := range ex.ends {
		if next.IsZero() || u.Before(next) {
			next = u
		}
	}
	var nextTransition *metav1.Time
	if !next.IsZero() {
		nextTransition = &metav1.Time{Time: next}
	}

	patch := client.MergeFrom(cw.DeepCopy())
	changed := cw.Status.Active != res.Active || cw.Status.Reason != res.Reason ||
		cw.Status.ObservedGeneration != cw.Generation || !equality.Semantic.DeepEqual(cw.Status.Exemptions, exemptions) ||
		!equality.Semantic.DeepEqual(cw.Status.NextTransition, nextTransition) ||
		!equality.Semantic.DeepEqual(cw.Status.ExemptionRecords, ex.records)
	cw.Status.Active = res.Active
	cw.Status.Reason = res.Reason
	cw.Status.ObservedGeneration = cw.Generation
	cw.Status.Exemptions = exemptions
	cw.Status.ExemptionRecords = ex.records
	cw.Status.NextTransition = nextTransition
	if meta.SetStatusCondition(&cw.Status.Conditions, validCondition(res, cw.Generation, now)) {
		changed = true
	}
	if setExemptionIgnored(&cw, ex.byAgents, now) {
		changed = true
	}
	if changed {
		if err := r.Status().Patch(ctx, &cw, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("patch changewindow status: %w", err)
		}
		log.Info().Bool("active", res.Active).Str("reason", res.Reason).Msg("changewindow status updated")
	}
	if newlyInvalid {
		log.Warn().Err(res.Err).Int64("generation", cw.Generation).
			Msg("invalid ChangeWindow; reported as active (blocking)")
		kubeevent.Emit(r.Recorder, &cw, corev1.EventTypeWarning, ReasonInvalidSpec, "Evaluate",
			res.Err.Error()+"; the window is active (blocking) until the spec is fixed")
	}

	if next.IsZero() {
		return ctrl.Result{}, nil
	}
	wait := next.Sub(now) + boundarySlack
	if wait > maxRequeue {
		wait = maxRequeue
	}
	return ctrl.Result{RequeueAfter: wait}, nil
}

// SetupWithManager registers the reconciler with the manager. Only spec or
// annotation changes trigger a reconcile; the reconciler's own status write does
// not, and boundaries are driven by RequeueAfter.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.ChangeWindow{}, builder.WithPredicates(eventfilter.SpecOrAnnotationChanged))
	return shard.Active().Complete(b, tracing.WrapReconciler("changewindow", r), &kardinalv1alpha1.ChangeWindowList{})
}

// ConditionValid is the ChangeWindow condition that reports whether the spec
// can be evaluated.
const ConditionValid = "Valid"

// ReasonInvalidSpec is the reason of the Valid=False condition and of the
// Warning Event for a spec that cannot be evaluated.
const ReasonInvalidSpec = "InvalidSpec"

// invalidAt reports whether the Valid condition of cw already says its spec
// was invalid at generation.
func invalidAt(cw kardinalv1alpha1.ChangeWindow, generation int64) bool {
	c := meta.FindStatusCondition(cw.Status.Conditions, ConditionValid)
	return c != nil && c.Status == metav1.ConditionFalse && c.ObservedGeneration == generation
}

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
		cond.Reason = ReasonInvalidSpec
		cond.Message = res.Err.Error() + "; the window is active (blocking) until the spec is fixed"
	}
	return cond
}

// DefaultMaxExemption is the --freeze-exemption-max-minutes default: the
// longest a freeze exemption counts from when the reconciler first saw it,
// as DefaultMaxOverride is for gate overrides.
const DefaultMaxExemption = 24 * time.Hour

func (r *Reconciler) maxExemption() time.Duration {
	if r.MaxExemption > 0 {
		return r.MaxExemption
	}
	return DefaultMaxExemption
}

// ConditionExemptionIgnored is True while spec.freeze.exemptions has entries
// made by an agent, which never count (break-glass is for people).
const ConditionExemptionIgnored = "ExemptionIgnored"

// exemptionState is a freeze's exemptions at one instant.
type exemptionState struct {
	// inEffect are the entries that count now.
	inEffect []kardinalv1alpha1.FreezeExemption
	// ends are when each entry in effect stops counting.
	ends []time.Time
	// records is the status record of every entry.
	records []kardinalv1alpha1.FreezeExemptionRecord
	// byAgents counts the entries an agent made.
	byAgents int
}

// freezeExemptions works out the exemptions of f at now from the records of
// when they were first seen (old): an entry counts until the earlier of its
// expiresAt and firstSeen plus maxExemption, unless an agent made it. A
// window that is not a freeze has none.
func freezeExemptions(f *kardinalv1alpha1.ChangeWindowFreeze, old []kardinalv1alpha1.FreezeExemptionRecord,
	now time.Time, maxExemption time.Duration) exemptionState {
	var st exemptionState
	if f == nil {
		return st
	}
	seen := make(map[string]kardinalv1alpha1.FreezeExemptionRecord, len(old))
	for _, rec := range old {
		seen[rec.Key] = rec
	}
	recorded := map[string]bool{}
	for i := range f.Exemptions {
		e := &f.Exemptions[i]
		key := ExemptionKey(e)
		rec, ok := seen[key]
		if !ok {
			rec = kardinalv1alpha1.FreezeExemptionRecord{Key: key, FirstSeen: metav1.NewTime(now.UTC().Truncate(time.Second))}
		}
		until := e.ExpiresAt.Time
		if capEnd := rec.FirstSeen.Add(maxExemption); capEnd.Before(until) {
			until = capEnd
		}
		rec.Until = metav1.NewTime(until.UTC())
		if !recorded[key] {
			recorded[key] = true
			st.records = append(st.records, rec)
		}
		if exemptionByAgent(e) {
			st.byAgents++
			continue
		}
		if now.Before(until) {
			st.inEffect = append(st.inEffect, *e)
			st.ends = append(st.ends, until)
		}
	}
	return st
}

// setExemptionIgnored sets ExemptionIgnored while agents' exemptions are
// listed, and removes it when none is. It reports whether it changed.
func setExemptionIgnored(cw *kardinalv1alpha1.ChangeWindow, byAgents int, now time.Time) bool {
	if byAgents == 0 {
		return meta.RemoveStatusCondition(&cw.Status.Conditions, ConditionExemptionIgnored)
	}
	return meta.SetStatusCondition(&cw.Status.Conditions, metav1.Condition{Type: ConditionExemptionIgnored,
		Status: metav1.ConditionTrue, Reason: "MadeByAgent", ObservedGeneration: cw.Generation,
		LastTransitionTime: metav1.NewTime(now),
		Message:            fmt.Sprintf("%d exemption(s) made by an agent do not count: break-glass is for people", byAgents)})
}

func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now().UTC()
}
