// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
)

// The AuditEvent actions of environment holds (#1528).
const (
	AuditActionHoldCreated  = "HoldCreated"
	AuditActionHoldReleased = "HoldReleased"
)

// DefaultHoldBundleGrace is how long a hold may name a Bundle that does not
// exist before it is Orphaned (#1629): kardinal rollback --hold writes the
// hold first and creates the Bundle right after, and the cache may lag the
// create.
const DefaultHoldBundleGrace = 2 * time.Minute

func (r *Reconciler) holdBundleGrace() time.Duration {
	if r.HoldBundleGrace > 0 {
		return r.HoldBundleGrace
	}
	return DefaultHoldBundleGrace
}

// holdStates returns status.holdStates for p's holds, and how long until
// the next BundleMissing hold turns Orphaned (0: none). A hold is Orphaned
// when its Bundle does not exist and the grace (--hold-bundle-grace) has passed since the
// hold's createdAt, or, without createdAt, since the controller first found
// the Bundle missing. A Bundle that exists again makes it Active again. The
// controller never edits spec.holds for it: the hold stays visible until it
// is released.
func (r *Reconciler) holdStates(ctx context.Context, p *kardinalv1alpha1.Pipeline, now time.Time) ([]kardinalv1alpha1.EnvironmentHoldState, time.Duration, error) {
	prev := map[string]*kardinalv1alpha1.EnvironmentHoldState{}
	for i := range p.Status.HoldStates {
		st := &p.Status.HoldStates[i]
		prev[st.Environment+"/"+st.Bundle] = st
	}
	var states []kardinalv1alpha1.EnvironmentHoldState
	var next time.Duration
	grace := r.holdBundleGrace()
	for i := range p.Spec.Holds {
		h := &p.Spec.Holds[i]
		st := kardinalv1alpha1.EnvironmentHoldState{Environment: h.Environment, Bundle: h.Bundle, State: kardinalv1alpha1.HoldStateActive}
		var b kardinalv1alpha1.Bundle
		err := r.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: h.Bundle}, &b)
		switch {
		case err == nil:
		case k8serrors.IsNotFound(err):
			since := metav1.NewTime(now.UTC().Truncate(time.Second))
			if old := prev[h.Environment+"/"+h.Bundle]; old != nil && old.BundleMissingSince != nil {
				since = *old.BundleMissingSince
			}
			st.BundleMissingSince = &since
			from := since.Time
			if h.CreatedAt != nil {
				from = h.CreatedAt.Time
			}
			if wait := from.Add(grace).Sub(now); wait > 0 {
				st.State = kardinalv1alpha1.HoldStateBundleMissing
				st.Message = fmt.Sprintf("rollback Bundle %s does not exist yet; the hold is not in effect from %s on unless it is created",
					h.Bundle, from.Add(grace).UTC().Format(time.RFC3339))
				if next == 0 || wait < next {
					next = wait
				}
			} else {
				st.State = kardinalv1alpha1.HoldStateOrphaned
				st.Message = fmt.Sprintf("rollback Bundle %s does not exist, so the hold is not in effect: other Bundles promote into %s. "+
					"Release it with kardinal release-hold %s --env %s, or hold again on another rollback",
					h.Bundle, h.Environment, p.Name, h.Environment)
			}
		default:
			return nil, 0, fmt.Errorf("get bundle %s of the hold of %s: %w", h.Bundle, h.Environment, err)
		}
		states = append(states, st)
	}
	return states, next, nil
}

// holdKey identifies one hold: the same environment held again on another
// Bundle, or at another time, is another hold.
func holdKey(h *kardinalv1alpha1.EnvironmentHold) string {
	at := ""
	if h.CreatedAt != nil {
		at = h.CreatedAt.UTC().Format(time.RFC3339)
	}
	return h.Environment + "|" + h.Bundle + "|" + at
}

// reconcileHolds keeps spec.holds and its record in status:
//   - a hold whose expiresAt has passed is removed from spec.holds (the
//     controller is the only writer that knows the time; the update starts
//     a new reconcile);
//   - every hold added since status.observedHolds gets a HoldCreated
//     AuditEvent and every hold removed a HoldReleased one, whichever client
//     changed spec.holds (CLI, UI, kubectl). The names are fixed per hold, so
//     a reconcile that runs again after a crash writes nothing new;
//   - status.observedHolds is set to spec.holds;
//   - status.holdStates says whether each hold is in effect (holdStates).
//
// It returns when to come back for the next expiry (0: none), and whether it
// updated the spec (the caller then stops: the update is reconciled anew).
func (r *Reconciler) reconcileHolds(ctx context.Context, log zerolog.Logger, p *kardinalv1alpha1.Pipeline) (time.Duration, bool, error) {
	now := r.now()
	var kept []kardinalv1alpha1.EnvironmentHold
	expired := false
	var next time.Duration
	for _, h := range p.Spec.Holds {
		if h.ExpiresAt != nil && !now.Before(h.ExpiresAt.Time) {
			expired = true
			log.Info().Str("env", h.Environment).Str("bundle", h.Bundle).Msg("hold expired — removing it")
			continue
		}
		if h.ExpiresAt != nil {
			if d := h.ExpiresAt.Sub(now); next == 0 || d < next {
				next = d
			}
		}
		kept = append(kept, h)
	}
	if expired {
		p.Spec.Holds = kept
		if err := r.Update(ctx, p); err != nil {
			return 0, false, fmt.Errorf("remove expired holds: %w", err)
		}
		return 0, true, nil
	}

	// Records stored by an earlier reconcile but not yet written go first
	// (#1552); while any remain the Pipeline is reconciled again soon.
	auditErr := r.flushAudit(ctx, p)

	observed := map[string]bool{}
	for i := range p.Status.ObservedHolds {
		observed[holdKey(&p.Status.ObservedHolds[i])] = true
	}
	var entries []kardinalv1alpha1.PendingAuditEvent
	current := map[string]bool{}
	for i := range p.Spec.Holds {
		h := &p.Spec.Holds[i]
		current[holdKey(h)] = true
		if !observed[holdKey(h)] {
			entries = append(entries, r.holdAuditEntry(p, h, AuditActionHoldCreated, fmt.Sprintf(
				"%s held %s on rollback %s: %s%s", orUnknown(h.CreatedBy), h.Environment, h.Bundle, h.Reason, expiresNote(h))))
		}
	}
	for i := range p.Status.ObservedHolds {
		h := &p.Status.ObservedHolds[i]
		if current[holdKey(h)] {
			continue
		}
		why := "released (spec.holds entry removed; the Kubernetes audit log, or the UI access log for a UI release, names who)"
		if h.ExpiresAt != nil && !now.Before(h.ExpiresAt.Time) {
			why = "expired at " + h.ExpiresAt.UTC().Format(time.RFC3339) + ", removed by the controller"
		}
		entries = append(entries, r.holdAuditEntry(p, h, AuditActionHoldReleased, fmt.Sprintf(
			"hold of %s on rollback %s (held by %s: %s) %s", h.Environment, h.Bundle, orUnknown(h.CreatedBy), h.Reason, why)))
	}
	states, orphanIn, err := r.holdStates(ctx, p, now)
	if err != nil {
		return 0, false, err
	}
	if orphanIn > 0 && (next == 0 || orphanIn < next) {
		next = orphanIn
	}
	for _, st := range states {
		if st.State == kardinalv1alpha1.HoldStateOrphaned && !p.HoldOrphaned(&kardinalv1alpha1.EnvironmentHold{Environment: st.Environment, Bundle: st.Bundle}) {
			log.Warn().Str("env", st.Environment).Str("bundle", st.Bundle).Msg("hold orphaned: its rollback Bundle does not exist; the hold is not in effect")
		}
	}
	if !equality.Semantic.DeepEqual(p.Status.ObservedHolds, p.Spec.Holds) || !equality.Semantic.DeepEqual(p.Status.HoldStates, states) {
		// The records go in the same patch as observedHolds, so a crash or a
		// failed create cannot lose them (#1552).
		// Locked on the resourceVersion p was read at: a reconcile from a
		// stale cache would see a hold a newer one already recorded as new
		// and store its record again.
		patch := client.MergeFromWithOptions(p.DeepCopy(), client.MergeFromWithOptimisticLock{})
		p.Status.ObservedHolds = append([]kardinalv1alpha1.EnvironmentHold(nil), p.Spec.Holds...)
		p.Status.HoldStates = states
		for _, e := range entries {
			p.Status.PendingAuditEvents = audit.Enqueue(ctx, auditKind, p.Status.PendingAuditEvents, e)
		}
		if err := r.Status().Patch(ctx, p, patch); err != nil {
			if k8serrors.IsConflict(err) {
				// Changed since it was read: the next reconcile works from
				// what is stored.
				log.Debug().Msg("pipeline changed since it was read; holds not recorded, retrying")
				return holdConflictRetry, false, nil
			}
			return 0, false, fmt.Errorf("patch observed holds: %w", err)
		}
		auditErr = r.flushAudit(ctx, p)
	}
	if auditErr != nil || len(p.Status.PendingAuditEvents) > 0 {
		if next == 0 || next > auditRetryDelay {
			next = auditRetryDelay
		}
	}
	return next, false, nil
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func expiresNote(h *kardinalv1alpha1.EnvironmentHold) string {
	if h.ExpiresAt == nil {
		return ""
	}
	return " (expires " + h.ExpiresAt.UTC().Format(time.RFC3339) + ")"
}

// auditKind labels the audit outbox metrics of Pipeline records.
const auditKind = "Pipeline"

// holdConflictRetry is how soon a reconcile whose holds patch lost to a
// newer write runs again.
const holdConflictRetry = time.Second

// auditRetryDelay is how soon a Pipeline whose audit outbox still holds
// unwritten records is reconciled again.
const auditRetryDelay = 5 * time.Second

// holdAuditEntry is the outbox entry of a hold change. Its name is fixed per
// hold and action, so writing it again is a no-op.
func (r *Reconciler) holdAuditEntry(p *kardinalv1alpha1.Pipeline, h *kardinalv1alpha1.EnvironmentHold,
	action, msg string) kardinalv1alpha1.PendingAuditEvent {
	sum := sha256.Sum256([]byte(holdKey(h)))
	suffix := "created"
	if action == AuditActionHoldReleased {
		suffix = "released"
	}
	prefix := p.Name
	if len(prefix) > 200 {
		prefix = prefix[:200]
	}
	return audit.Entry(fmt.Sprintf("%s-hold-%s-%s", prefix, hex.EncodeToString(sum[:])[:10], suffix),
		map[string]string{
			"kardinal.io/pipeline":    p.Name,
			"kardinal.io/bundle":      h.Bundle,
			"kardinal.io/environment": h.Environment,
			"kardinal.io/action":      action,
		}, kardinalv1alpha1.AuditEventSpec{
			BundleName:   h.Bundle,
			PipelineName: p.Name,
			Environment:  h.Environment,
			Action:       action,
			Outcome:      "Success",
			Message:      msg,
		}, metav1.NewTime(r.now()))
}

// flushAudit creates the AuditEvents in p's outbox and removes the written
// entries from its status (optimistic lock, so an entry a newer reconcile
// stored is not dropped). The record must not block the Pipeline: an
// unwritten entry stays and is retried.
func (r *Reconciler) flushAudit(ctx context.Context, p *kardinalv1alpha1.Pipeline) error {
	if len(p.Status.PendingAuditEvents) == 0 {
		return nil
	}
	remaining, ferr := audit.Flush(ctx, r.Client, auditKind, p.Namespace, p.Status.PendingAuditEvents)
	if len(remaining) != len(p.Status.PendingAuditEvents) {
		prev := p.Status.PendingAuditEvents
		patch := client.MergeFromWithOptions(p.DeepCopy(), client.MergeFromWithOptimisticLock{})
		p.Status.PendingAuditEvents = remaining
		if err := r.Status().Patch(ctx, p, patch); err != nil && !k8serrors.IsNotFound(err) {
			p.Status.PendingAuditEvents = prev
			return errors.Join(ferr, fmt.Errorf("remove written AuditEvents from the outbox: %w", err))
		}
	}
	if ferr != nil {
		zerolog.Ctx(ctx).Error().Err(ferr).Int("pending", len(remaining)).
			Msg("failed to write AuditEvent; kept in status.pendingAuditEvents to retry")
	}
	return ferr
}
