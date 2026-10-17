// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
)

// AuditActionFreezeExempted is written once when a step goes on through an
// active freeze under an exemption (kardinal override --freeze).
const AuditActionFreezeExempted = "FreezeExempted"

// condFreezeExempted is the step condition that records the exemption the
// step went on under, so it is audited once.
const condFreezeExempted = "FreezeExempted"

// holdIfFrozen keeps a step where it is while an active freeze holds it
// (docs/design/18-actor-policy.md section 9.2): a ChangeWindow with
// spec.freeze that is active now, selects the step's namespace and, for an
// agent freeze, a version an agent created, and has no exemption for it.
//
// It runs where holdIfPaused runs: before a Pending step starts and before a
// Promoting step's next git step. Steps waiting for a merge or a health
// check carry on, as with pause. A step let through by exemptions writes
// one FreezeExempted AuditEvent per freeze that exempts it. The step writes only its own status; a
// read error holds it (a kill switch fails closed).
func (r *Reconciler) holdIfFrozen(ctx context.Context, log zerolog.Logger, ps *v1alpha1.PromotionStep) (bool, ctrl.Result, error) {
	matches, err := r.freezesFor(ctx, ps)
	if err != nil {
		return true, ctrl.Result{}, err
	}
	held := changewindow.Holding(matches)
	if len(held) == 0 && ps.Status.FrozenBy != nil {
		// No freeze holds it any more: clear the hold first, then go on in
		// a new reconcile, so the step's next status write (locked on the
		// resourceVersion it read) starts from the cleared one.
		if err := r.clearFrozenBy(ctx, ps); err != nil {
			return true, ctrl.Result{}, err
		}
		return true, ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if len(held) == 0 {
		if len(matches) == 0 {
			return false, ctrl.Result{}, nil
		}
		// Let through under an exemption: record it first, then go on in a
		// new reconcile, so the step's next status write starts from the
		// recorded one (its patches carry the resourceVersion they read).
		recorded, err := r.recordFreezeExemption(ctx, ps, matches, r.now())
		if err != nil {
			return true, ctrl.Result{}, err
		}
		if recorded {
			return true, ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return false, ctrl.Result{}, nil
	}
	msg := changewindow.FrozenMessage(held[0], ps.Spec.PipelineName, ps.Spec.Environment)
	by := changewindow.StepFreezeOf(held[0])
	if ps.Status.Message != msg || !equality.Semantic.DeepEqual(ps.Status.FrozenBy, by) {
		patch := client.MergeFrom(ps.DeepCopy())
		ps.Status.Message = msg
		ps.Status.FrozenBy = by
		if err := r.Status().Patch(ctx, ps, patch); err != nil {
			return true, ctrl.Result{}, fmt.Errorf("patch frozen message: %w", err)
		}
		log.Info().Str("changewindow", held[0].Window.Name).Str("env", ps.Spec.Environment).
			Str("state", ps.Status.State).Msg("freeze active — step held")
	}
	// The ChangeWindow watch wakes the step when the freeze ends or an
	// exemption takes effect; the minute is the fallback.
	return true, ctrl.Result{RequeueAfter: requeuePaused}, nil
}

// clearFrozenBy removes status.frozenBy from a step no freeze holds any
// more.
func (r *Reconciler) clearFrozenBy(ctx context.Context, ps *v1alpha1.PromotionStep) error {
	patch := client.MergeFrom(ps.DeepCopy())
	ps.Status.FrozenBy = nil
	if err := r.Status().Patch(ctx, ps, patch); err != nil {
		return fmt.Errorf("clear status.frozenBy: %w", err)
	}
	return nil
}

// freezesFor returns the active freezes that select ps
// (changewindow.FreezesFor, from the windows' status). The version is read
// only when an agent freeze exists.
func (r *Reconciler) freezesFor(ctx context.Context, ps *v1alpha1.PromotionStep) ([]changewindow.FreezeMatch, error) {
	var windows v1alpha1.ChangeWindowList
	if err := r.List(ctx, &windows); err != nil {
		return nil, fmt.Errorf("list ChangeWindows for freezes: %w", err)
	}
	t := changewindow.FreezeTarget{Namespace: ps.Namespace, Pipeline: ps.Spec.PipelineName,
		Environment: ps.Spec.Environment, Bundle: ps.Spec.BundleName}
	if needsAuthor(windows.Items) {
		b, err := r.loadBundle(ctx, ps)
		if err != nil {
			return nil, fmt.Errorf("read the version's actor for an agent freeze: %w", err)
		}
		t.Author = lifecycle.BundleAuthor(b)
	}
	return changewindow.FreezesFor(windows.Items, t), nil
}

// needsAuthor reports whether an agent freeze exists, so the version's actor
// decides whether it holds.
func needsAuthor(windows []v1alpha1.ChangeWindow) bool {
	for i := range windows {
		if f := windows[i].Spec.Freeze; f != nil && f.Actors != v1alpha1.FreezeEveryone {
			return true
		}
	}
	return false
}

// recordFreezeExemption records, once per set of exemptions, that ps went on
// through the freezes in matches (each exempts it): the FreezeExempted
// condition and, in the same optimistic-locked status patch, one AuditEvent
// per freeze in the step's outbox. It reports whether it wrote anything.
func (r *Reconciler) recordFreezeExemption(ctx context.Context, ps *v1alpha1.PromotionStep,
	matches []changewindow.FreezeMatch, now time.Time) (bool, error) {
	msgs := make([]string, len(matches))
	for i, m := range matches {
		msgs[i] = exemptionMessage(m)
	}
	msg := strings.Join(msgs, "; ")
	if c := meta.FindStatusCondition(ps.Status.Conditions, condFreezeExempted); c != nil && c.Message == msg {
		return false, nil
	}
	patch := client.MergeFromWithOptions(ps.DeepCopy(), client.MergeFromWithOptimisticLock{})
	meta.SetStatusCondition(&ps.Status.Conditions, metav1.Condition{Type: condFreezeExempted,
		Status: metav1.ConditionTrue, Reason: "Exempted", Message: msg, ObservedGeneration: ps.Generation})
	for i, m := range matches {
		if entry, ok := freezeAuditEntry(ps, m, msgs[i], now); ok {
			ps.Status.PendingAuditEvents = audit.Enqueue(ctx, auditKind, ps.Status.PendingAuditEvents, entry)
		}
	}
	if err := r.Status().Patch(ctx, ps, patch); err != nil {
		return false, fmt.Errorf("record freeze exemption: %w", err)
	}
	_ = r.flushAudit(ctx, ps)
	return true, nil
}

// exemptionMessage says that a step went on through freeze m: by whom, until
// when (the exemption's end, the controller's cap applied) and why.
func exemptionMessage(m changewindow.FreezeMatch) string {
	e := m.Exemption
	by := e.CreatedBy
	if by == "" {
		by = "(unknown)"
	}
	until := m.Until
	if until.IsZero() {
		until = e.ExpiresAt.Time
	}
	return fmt.Sprintf("let through freeze %s (%s) by %s until %s: %s", m.Window.Name,
		changewindow.FreezeScope(m.Window.Spec.Freeze), by, until.UTC().Format(time.RFC3339), e.Reason)
}

// freezeAuditEntry is the FreezeExempted outbox entry. Its name is derived
// from the step, the window and the exemption, so a re-run finds it.
func freezeAuditEntry(ps *v1alpha1.PromotionStep, m changewindow.FreezeMatch, msg string,
	now time.Time) (v1alpha1.PendingAuditEvent, bool) {
	e, ok := auditEntry(ps, AuditActionFreezeExempted, AuditOutcomeSuccess, msg, now, nil)
	if !ok {
		return e, false
	}
	x := m.Exemption
	sum := sha256.Sum256([]byte(m.Window.Name + "\x00" + x.CreatedBy + "\x00" + x.Environment + "\x00" +
		x.Bundle + "\x00" + x.ExpiresAt.UTC().Format(time.RFC3339) + "\x00" + x.Reason))
	suffix := "-freeze-exempted-" + hex.EncodeToString(sum[:])[:10]
	base := ps.Name
	if limit := 253 - len(suffix); len(base) > limit {
		base = base[:limit]
	}
	e.Name = sanitizeK8sName(base + suffix)
	return e, true
}

// changeWindowStepRequests maps a freezing ChangeWindow event to the steps a
// freeze can hold or release: Pending and Promoting steps in the namespaces
// it selects, so a freeze and an unfreeze take effect at once (requeuePaused
// is the fallback).
func (r *Reconciler) changeWindowStepRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	cw, ok := obj.(*v1alpha1.ChangeWindow)
	if !ok || cw.Spec.Freeze == nil {
		return nil
	}
	var steps v1alpha1.PromotionStepList
	if err := r.List(ctx, &steps); err != nil {
		zerolog.Ctx(ctx).Error().Err(err).Str("changewindow", cw.Name).
			Msg("list PromotionSteps for a freeze; held steps re-check within a minute")
		return nil
	}
	var out []reconcile.Request
	for i := range steps.Items {
		s := &steps.Items[i]
		if s.Status.State != "" && s.Status.State != StatePending && s.Status.State != StatePromoting {
			continue
		}
		if !changewindow.FreezeSelects(&v1alpha1.ChangeWindowFreeze{Actors: v1alpha1.FreezeEveryone,
			Namespaces: cw.Spec.Freeze.Namespaces}, changewindow.FreezeTarget{Namespace: s.Namespace}) {
			continue
		}
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: s.Namespace, Name: s.Name}})
	}
	return out
}
