// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
)

// The AuditEvent actions of environment holds (#1528).
const (
	AuditActionHoldCreated  = "HoldCreated"
	AuditActionHoldReleased = "HoldReleased"
)

// DefaultHoldBundleGrace is how long a hold may name a Bundle that does not
// exist before the controller reports it (#1629): kardinal rollback --hold
// writes the hold first and creates the Bundle right after, and the cache
// may lag the create. The hold stays in effect after it too: the controller
// never lifts a hold.
const DefaultHoldBundleGrace = 2 * time.Minute

// AuditActionHoldBundleMissing is the AuditEvent of a hold whose rollback
// Bundle has not existed for the grace.
const AuditActionHoldBundleMissing = "HoldBundleMissing"

// condHoldBundleMissing is the Pipeline condition that reports holds whose
// rollback Bundle is missing.
const condHoldBundleMissing = "HoldBundleMissing"

func (r *Reconciler) holdBundleGrace() time.Duration {
	if r.HoldBundleGrace > 0 {
		return r.HoldBundleGrace
	}
	return DefaultHoldBundleGrace
}

// releaseCommand is how a human ends hold h of p.
func releaseCommand(p *kardinalv1alpha1.Pipeline, h *kardinalv1alpha1.EnvironmentHold) string {
	return fmt.Sprintf("kardinal release-hold %s --env %s", p.Name, h.Environment)
}

// holdCheck is what holdStates found.
type holdCheck struct {
	states []kardinalv1alpha1.EnvironmentHoldState
	// next is how long until the next missing Bundle's grace ends (0: none).
	next time.Duration
	// reported are the holds whose missing Bundle is reported in this
	// reconcile: once each (ReportedAt).
	reported []*kardinalv1alpha1.EnvironmentHold
	// lookupErrs are Bundle reads that failed for another reason than
	// NotFound; those holds keep their previous state.
	lookupErrs []string
}

// holdStates checks that the rollback Bundle of each hold exists. A missing
// one is BundleMissing from the controller's first sighting (the client-set
// createdAt is not trusted). The hold stays in effect whatever its state:
// past the grace (--hold-bundle-grace) the controller reports it once
// (ReportedAt; the caller writes the condition, Event, AuditEvent and
// metric) and a human releases or replaces it. A Bundle that exists again
// makes the hold Active.
func (r *Reconciler) holdStates(ctx context.Context, p *kardinalv1alpha1.Pipeline, now time.Time) holdCheck {
	var out holdCheck
	grace := r.holdBundleGrace()
	at := metav1.NewTime(now.UTC().Truncate(time.Second))
	for i := range p.Spec.Holds {
		h := &p.Spec.Holds[i]
		prev := p.HoldState(h)
		st := kardinalv1alpha1.EnvironmentHoldState{Environment: h.Environment, Bundle: h.Bundle, State: kardinalv1alpha1.HoldStateActive}
		var b kardinalv1alpha1.Bundle
		err := r.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: h.Bundle}, &b)
		switch {
		case err == nil:
		case k8serrors.IsNotFound(err):
			st.State = kardinalv1alpha1.HoldStateBundleMissing
			since := at
			if prev != nil && prev.BundleMissingSince != nil {
				since = *prev.BundleMissingSince
			}
			st.BundleMissingSince = &since
			if prev != nil {
				st.ReportedAt = prev.ReportedAt
			}
			if wait := since.Add(grace).Sub(now); wait > 0 {
				st.Message = fmt.Sprintf("rollback Bundle %s does not exist (missing since %s); the hold stays in effect",
					h.Bundle, since.UTC().Format(time.RFC3339))
				if out.next == 0 || wait < out.next {
					out.next = wait
				}
			} else {
				st.Message = fmt.Sprintf("rollback Bundle %s does not exist (missing since %s); the hold stays in effect "+
					"and no other Bundle promotes into %s. Release it with: %s, or replace it with a new rollback --hold",
					h.Bundle, since.UTC().Format(time.RFC3339), h.Environment, releaseCommand(p, h))
				if st.ReportedAt == nil {
					st.ReportedAt = &at
					out.reported = append(out.reported, h)
				}
			}
		default:
			out.lookupErrs = append(out.lookupErrs, fmt.Sprintf("%s: get rollback Bundle %s: %v", h.Environment, h.Bundle, err))
			if prev != nil {
				st = *prev
			}
		}
		out.states = append(out.states, st)
	}
	return out
}

// setHoldCondition writes the HoldBundleMissing condition: True while a
// reported hold's Bundle is missing (past the grace), Unknown when a Bundle
// could not be read, False otherwise. A Pipeline that never had a hold gets
// none.
func setHoldCondition(p *kardinalv1alpha1.Pipeline, chk holdCheck) {
	var missing []string
	for i := range chk.states {
		st := &chk.states[i]
		if st.State == kardinalv1alpha1.HoldStateBundleMissing && st.ReportedAt != nil {
			missing = append(missing, st.Environment+": "+st.Message)
		}
	}
	cond := metav1.Condition{Type: condHoldBundleMissing, ObservedGeneration: p.Generation}
	switch {
	case len(missing) > 0:
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, "BundleMissing", strings.Join(missing, "; ")
	case len(chk.lookupErrs) > 0:
		cond.Status, cond.Reason = metav1.ConditionUnknown, "LookupFailed"
		cond.Message = "the holds are kept; " + strings.Join(chk.lookupErrs, "; ")
	default:
		if len(p.Spec.Holds) == 0 && meta.FindStatusCondition(p.Status.Conditions, condHoldBundleMissing) == nil {
			return
		}
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "NotReported",
			"no hold's rollback Bundle has been missing for longer than the grace"
	}
	meta.SetStatusCondition(&p.Status.Conditions, cond)
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
//   - status.holdStates says whether each hold's rollback Bundle exists, and
//     a hold missing it past the grace is reported once (holdStates).
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
	chk := r.holdStates(ctx, p, now)
	if chk.next > 0 && (next == 0 || chk.next < next) {
		next = chk.next
	}
	for _, e := range chk.lookupErrs {
		log.Warn().Str("error", e).Msg("hold: could not read a rollback Bundle; the hold is kept")
	}
	for _, h := range chk.reported {
		entries = append(entries, r.holdAuditEntry(p, h, AuditActionHoldBundleMissing, fmt.Sprintf(
			"rollback Bundle %s of the hold of %s (held by %s: %s) does not exist; the hold stays in effect until: %s",
			h.Bundle, h.Environment, orUnknown(h.CreatedBy), h.Reason, releaseCommand(p, h))))
	}
	before := p.DeepCopy()
	setHoldCondition(p, chk)
	condChanged := !equality.Semantic.DeepEqual(before.Status.Conditions, p.Status.Conditions)
	p.Status.Conditions = before.Status.Conditions
	if !equality.Semantic.DeepEqual(p.Status.ObservedHolds, p.Spec.Holds) || !equality.Semantic.DeepEqual(p.Status.HoldStates, chk.states) || condChanged {
		// The records go in the same patch as observedHolds, so a crash or a
		// failed create cannot lose them (#1552).
		// Locked on the resourceVersion p was read at: a reconcile from a
		// stale cache would see a hold a newer one already recorded as new
		// and store its record again.
		patch := client.MergeFromWithOptions(p.DeepCopy(), client.MergeFromWithOptimisticLock{})
		p.Status.ObservedHolds = append([]kardinalv1alpha1.EnvironmentHold(nil), p.Spec.Holds...)
		p.Status.HoldStates = chk.states
		setHoldCondition(p, chk)
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
		// Once the report is stored (ReportedAt), so it is not repeated.
		for _, h := range chk.reported {
			observability.HoldBundleMissingTotal.WithLabelValues(p.Namespace, p.Name).Inc()
			kubeevent.Emit(r.Recorder, p, corev1.EventTypeWarning, condHoldBundleMissing, "CheckHold",
				fmt.Sprintf("rollback Bundle %s of the hold of %s does not exist; the hold stays in effect until: %s",
					h.Bundle, h.Environment, releaseCommand(p, h)))
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
	suffix, outcome := "created", "Success"
	switch action {
	case AuditActionHoldReleased:
		suffix = "released"
	case AuditActionHoldBundleMissing:
		suffix, outcome = "bundle-missing", "Failure"
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
			Outcome:      outcome,
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
