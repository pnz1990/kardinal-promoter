// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
)

// AuditActionGateOverridden is the AuditEvent action written once for each
// override recorded on a gate instance (kardinal override or the UI, #1450).
const AuditActionGateOverridden = "GateOverridden"

// conditionOverrideIgnored is True while spec.overrides has entries the
// controller does not count: dated after it first saw them, or past the
// record limit.
const conditionOverrideIgnored = "OverrideIgnored"

// overrideClockSkew is how much later than firstSeen an override's createdAt
// may be (clock differences between the writer and the controller).
const overrideClockSkew = 5 * time.Minute

// maxOverrideRecords bounds status.overrides (the CRD's maxItems). Records
// are never dropped, so past it a new override is not counted.
const maxOverrideRecords = 200

// DefaultMaxOverride is the --gate-override-max-minutes default: the longest
// an override counts from when the controller first saw it.
const DefaultMaxOverride = 24 * time.Hour

func (r *Reconciler) maxOverride() time.Duration {
	if r.MaxOverride > 0 {
		return r.MaxOverride
	}
	return DefaultMaxOverride
}

// overrideKey identifies one spec.overrides entry: a hash of every field, so
// an edited entry is a new override. It is the key of its status.overrides
// record and the suffix of its AuditEvent name.
func overrideKey(o *kardinalv1alpha1.PolicyGateOverride) string {
	created := ""
	if o.CreatedAt != nil {
		created = o.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s",
		o.CreatedBy, created, o.Stage, o.ExpiresAt.UTC().Format(time.RFC3339Nano), o.Reason)))
	return hex.EncodeToString(sum[:])[:12]
}

// OverrideKey is the status.overrides key of override o.
func OverrideKey(o *kardinalv1alpha1.PolicyGateOverride) string { return overrideKey(o) }

// overrideRecords indexes status.overrides by key.
func overrideRecords(gate *kardinalv1alpha1.PolicyGate) map[string]kardinalv1alpha1.OverrideRecord {
	m := make(map[string]kardinalv1alpha1.OverrideRecord, len(gate.Status.Overrides))
	for _, rec := range gate.Status.Overrides {
		m[rec.Key] = rec
	}
	return m
}

// futureDated reports whether o's createdAt is more than the clock skew after
// the controller first saw it: a writer dating an entry ahead to chain
// overrides past the cap.
func futureDated(o *kardinalv1alpha1.PolicyGateOverride, firstSeen time.Time) bool {
	return o.CreatedAt != nil && o.CreatedAt.After(firstSeen.Add(overrideClockSkew))
}

// overrideEnd is when override o, first seen at firstSeen, stops counting:
// the earlier of its expiresAt and firstSeen plus the cap.
func overrideEnd(o *kardinalv1alpha1.PolicyGateOverride, firstSeen time.Time, maxOverride time.Duration) time.Time {
	end := o.ExpiresAt.Time
	if capEnd := firstSeen.Add(maxOverride); maxOverride > 0 && capEnd.Before(end) {
		end = capEnd
	}
	return end
}

// activeOverride is the override in effect on a gate and its record.
type activeOverride struct {
	override *kardinalv1alpha1.PolicyGateOverride
	record   kardinalv1alpha1.OverrideRecord
	end      time.Time
}

// reason is the gate's status.reason while the override is in effect.
func (a activeOverride) reason() string {
	by := a.override.CreatedBy
	if !a.record.Verified {
		by += " (unverified)"
	}
	return fmt.Sprintf("OVERRIDDEN by %s: %s (expires %s)", by, a.override.Reason, a.end.UTC().Format("2006-01-02T15:04Z"))
}

// findActiveOverride returns the first override matching envName (an
// override with Stage "" matches any) that is still in effect. Only recorded
// overrides count (recordOverrides runs first), each from its firstSeen; a
// future-dated one never counts.
func findActiveOverride(gate *kardinalv1alpha1.PolicyGate, envName string, now time.Time,
	maxOverride time.Duration) (activeOverride, bool) {
	records := overrideRecords(gate)
	for i := range gate.Spec.Overrides {
		o := &gate.Spec.Overrides[i]
		rec, ok := records[overrideKey(o)]
		if !ok || o.ExpiresAt.IsZero() || futureDated(o, rec.FirstSeen.Time) {
			continue
		}
		end := overrideEnd(o, rec.FirstSeen.Time, maxOverride)
		if !now.Before(end) {
			continue
		}
		if o.Stage == "" || o.Stage == envName {
			return activeOverride{override: o, record: rec, end: end}, true
		}
	}
	return activeOverride{}, false
}

// recordOverrides keeps status.overrides, the one record of each override
// the controller has seen, and writes each override's GateOverridden
// AuditEvent once.
//
//  1. A new entry gets a record: firstSeen now, verified when the identity
//     admission policy is bound (IdentityPolicyActive) and the gate was
//     already checked (overridesVerifiedSince, or a gate never evaluated: a
//     gate instance has no overrides when kro creates it). The record is
//     written first, with an optimistic lock, so a reconcile from a stale
//     cache records nothing a second time (#1513).
//  2. An unaudited record gets its AuditEvent, stamped firstSeen; its name
//     is derived from the gate and the key, so a retry after a crash finds
//     it (AlreadyExists) and writes nothing new. Then the record is marked
//     audited. An AuditEvent that cannot be written is retried on the next
//     reconcile; audit never blocks gate evaluation.
//
// Records are never dropped: an entry removed and added again keeps its
// firstSeen, so it cannot restart the cap. The OverrideIgnored condition
// names entries that are not counted.
//
// Graph-first: the PolicyGate reconciler writes only its own status and the
// append-only AuditEvent, from its own spec.
func (r *Reconciler) recordOverrides(ctx context.Context, gate *kardinalv1alpha1.PolicyGate) error {
	now := r.now()
	records := overrideRecords(gate)
	verified := r.identityPolicyActive(ctx) &&
		(gate.Status.OverridesVerifiedSince != nil || gate.Status.LastEvaluatedAt == nil)
	next := slices.Clone(gate.Status.Overrides)
	var ignored []string
	for i := range gate.Spec.Overrides {
		o := &gate.Spec.Overrides[i]
		key := overrideKey(o)
		rec, ok := records[key]
		if !ok {
			if len(next) >= maxOverrideRecords {
				ignored = append(ignored, fmt.Sprintf("%q by %s: more than %d overrides recorded on this gate",
					o.Reason, o.CreatedBy, maxOverrideRecords))
				continue
			}
			rec = kardinalv1alpha1.OverrideRecord{Key: key, FirstSeen: metav1.NewTime(now), Verified: verified}
			records[key] = rec
			next = append(next, rec)
		}
		if futureDated(o, rec.FirstSeen.Time) {
			ignored = append(ignored, fmt.Sprintf("%q by %s created %s, more than %s after the controller first saw it",
				o.Reason, o.CreatedBy, o.CreatedAt.UTC().Format(time.RFC3339), overrideClockSkew))
		}
	}
	cond := metav1.Condition{Type: conditionOverrideIgnored, Status: metav1.ConditionFalse, Reason: "NoneIgnored",
		Message: "every override counts from when the controller first saw it", ObservedGeneration: gate.Generation}
	if len(ignored) > 0 {
		cond.Status, cond.Reason = metav1.ConditionTrue, "NotCounted"
		cond.Message = truncateMessage(fmt.Sprintf("%d override(s) not counted: %s", len(ignored), strings.Join(ignored, "; ")))
	}
	prev := meta.FindStatusCondition(gate.Status.Conditions, conditionOverrideIgnored)
	condChanged := (len(gate.Spec.Overrides) > 0 || prev != nil) &&
		(prev == nil || prev.Status != cond.Status || prev.Reason != cond.Reason || prev.Message != cond.Message)
	// A gate evaluated before (an upgrade) gets its verifiedSince stamp now,
	// once; a new gate gets it with its first evaluation (patchStatus).
	stamp := gate.Status.OverridesVerifiedSince == nil && gate.Status.LastEvaluatedAt != nil
	if len(next) != len(gate.Status.Overrides) || condChanged || stamp {
		patch := client.MergeFromWithOptions(gate.DeepCopy(), client.MergeFromWithOptimisticLock{})
		gate.Status.Overrides = next
		if condChanged {
			cond.LastTransitionTime = metav1.NewTime(now)
			meta.SetStatusCondition(&gate.Status.Conditions, cond)
		}
		stampVerifiedSince(gate, now)
		if err := r.Status().Patch(ctx, gate, patch); err != nil {
			return fmt.Errorf("record overrides: %w", err)
		}
	}
	return r.auditRecordedOverrides(ctx, gate)
}

// auditRecordedOverrides stores the GateOverridden record of every
// recorded, unaudited override still in spec.overrides in the gate's audit
// outbox (#1552), in the same optimistic-locked patch that marks the
// override audited, then writes the outbox.
func (r *Reconciler) auditRecordedOverrides(ctx context.Context, gate *kardinalv1alpha1.PolicyGate) error {
	records := overrideRecords(gate)
	audited := map[string]bool{}
	var entries []kardinalv1alpha1.PendingAuditEvent
	for i := range gate.Spec.Overrides {
		o := &gate.Spec.Overrides[i]
		key := overrideKey(o)
		rec, ok := records[key]
		if !ok || rec.Audited || audited[key] {
			continue
		}
		if e, ok := r.overrideAuditEntry(gate, o, rec); ok {
			entries = append(entries, e)
		}
		audited[key] = true
	}
	if len(audited) == 0 {
		return nil
	}
	patch := client.MergeFromWithOptions(gate.DeepCopy(), client.MergeFromWithOptimisticLock{})
	for i := range gate.Status.Overrides {
		if audited[gate.Status.Overrides[i].Key] {
			gate.Status.Overrides[i].Audited = true
		}
	}
	for _, e := range entries {
		gate.Status.PendingAuditEvents = audit.Enqueue(ctx, auditKind, gate.Status.PendingAuditEvents, e)
	}
	if err := r.Status().Patch(ctx, gate, patch); err != nil {
		return fmt.Errorf("mark overrides audited: %w", err)
	}
	_ = r.flushAudit(ctx, gate)
	return nil
}

// OverrideVerified reports whether the createdBy of override o on gate was
// checked by the identity admission policy (status.overrides). An override
// the controller has not recorded is unverified.
func OverrideVerified(gate *kardinalv1alpha1.PolicyGate, o *kardinalv1alpha1.PolicyGateOverride) bool {
	rec, ok := overrideRecords(gate)[overrideKey(o)]
	return ok && rec.Verified
}

// stampVerifiedSince sets status.overridesVerifiedSince to now the first
// time, in memory; the caller's status patch writes it.
func stampVerifiedSince(gate *kardinalv1alpha1.PolicyGate, now time.Time) {
	if gate.Status.OverridesVerifiedSince == nil {
		t := metav1.NewTime(now)
		gate.Status.OverridesVerifiedSince = &t
	}
}

// overrideAuditEntry is the outbox entry of the GateOverridden AuditEvent
// of override o of gate, and false for a gate a Graph did not create. Its
// timestamp is the record's firstSeen, not the createdAt the writer
// supplied, and it names the capped end.
func (r *Reconciler) overrideAuditEntry(gate *kardinalv1alpha1.PolicyGate,
	o *kardinalv1alpha1.PolicyGateOverride, rec kardinalv1alpha1.OverrideRecord) (kardinalv1alpha1.PendingAuditEvent, bool) {
	labels := gate.GetLabels()
	if labels[labelPipeline] == "" || labels[labelBundle] == "" || labels[labelEnvironment] == "" {
		return kardinalv1alpha1.PendingAuditEvent{}, false // nothing to attribute the record to
	}
	end := overrideEnd(o, rec.FirstSeen.Time, r.maxOverride())
	stage := o.Stage
	if stage == "" {
		stage = "every stage"
	}
	by := o.CreatedBy
	if by == "" {
		by = "(unknown)"
	}
	if !rec.Verified {
		by += " (unverified: the identity admission policy did not check it)"
	}
	suffix := "-override-" + rec.Key
	base := gate.Name
	if limit := maxObjectNameLength - len(suffix); len(base) > limit {
		base = base[:limit]
	}
	return audit.Entry(sanitizeGateName(base+suffix), gateAuditLabels(labels, AuditActionGateOverridden),
		kardinalv1alpha1.AuditEventSpec{
			BundleName:   labels[labelBundle],
			PipelineName: labels[labelPipeline],
			Environment:  labels[labelEnvironment],
			Action:       AuditActionGateOverridden,
			Outcome:      "Success",
			Message: fmt.Sprintf("gate %s overridden by %s for %s until %s: %s",
				gate.Name, by, stage, end.UTC().Format(time.RFC3339), o.Reason),
		}, rec.FirstSeen), true
}
