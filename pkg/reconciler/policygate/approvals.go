// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// approvalTally is lifecycle.ApprovalTally, the tally the CLI and the UI
// API use too, with the reconciler's views of it (blocked, met, context).
type approvalTally struct {
	lifecycle.ApprovalTally
}

// tallyApprovals counts the decisions of gate's spec.approvals for its
// Bundle and environment (lifecycle.TallyApprovals): the shared rules, so
// what the CLI says about who may approve matches what this counts.
func tallyApprovals(gate *kardinalv1alpha1.PolicyGate, creator string) approvalTally {
	return approvalTally{lifecycle.TallyApprovals(gate, creator)}
}

// context is the approvals CEL variable: approvals.count (counted approvers),
// approvals.required, approvals.users and approvals.rejected.
func (t approvalTally) context() map[string]interface{} {
	users := make([]interface{}, len(t.Approvers))
	for i, u := range t.Approvers {
		users[i] = u
	}
	return map[string]interface{}{
		"count":    int64(len(t.Approvers)),
		"required": int64(t.Required),
		"users":    users,
		"rejected": len(t.Rejecters) > 0,
	}
}

// blocked returns why the approval policy holds the gate, or "" when it is
// met or the gate has no policy.
func (t approvalTally) blocked() string {
	if t.Required == 0 {
		return ""
	}
	if t.Held != "" {
		return t.Held
	}
	if len(t.Rejecters) > 0 {
		var parts []string
		for _, u := range t.Rejecters {
			if c := t.Comments[u]; c != "" {
				parts = append(parts, fmt.Sprintf("%s (%s)", u, c))
			} else {
				parts = append(parts, u)
			}
		}
		return "rejected by " + strings.Join(parts, ", ")
	}
	if len(t.Approvers) < t.Required {
		msg := fmt.Sprintf("waiting for approvals: %d of %d", len(t.Approvers), t.Required)
		if len(t.Approvers) > 0 {
			msg += " (" + strings.Join(t.Approvers, ", ") + ")"
		}
		return msg
	}
	return ""
}

// met returns the reason suffix of a gate whose policy is met.
func (t approvalTally) met() string {
	if t.Required == 0 {
		return ""
	}
	return fmt.Sprintf("approved by %s (%d of %d)", strings.Join(t.Approvers, ", "), len(t.Approvers), t.Required)
}

// recordApprovals writes the tally's records to status.approvals when they
// changed. firstSeenAt is kept for a decision already recorded (same user and
// decision) and set to now for a new one. Graph-first: the gate's own status,
// from its own spec.
func (r *Reconciler) recordApprovals(ctx context.Context, gate *kardinalv1alpha1.PolicyGate, t approvalTally) error {
	now := metav1.NewTime(r.now())
	records := make([]kardinalv1alpha1.GateApprovalStatus, len(t.Records))
	for i, rec := range t.Records {
		rec.FirstSeenAt = &now
		for _, old := range gate.Status.Approvals {
			if old.User == rec.User && old.Decision == rec.Decision && old.FirstSeenAt != nil {
				rec.FirstSeenAt = old.FirstSeenAt
				break
			}
		}
		records[i] = rec
	}
	if len(records) == 0 {
		records = nil
	}
	if equality.Semantic.DeepEqual(records, gate.Status.Approvals) {
		return nil
	}
	// The patch carries the resourceVersion the gate was read at, as
	// patchStatus's does (#1513): a reconcile from a stale cache, which would
	// see the decisions a newer reconcile already recorded as new and audit
	// them again, gets a Conflict and writes nothing. The audit records
	// follow the status write, so only the reconcile that recorded a change
	// audits it.
	old, oldPending := gate.Status.Approvals, gate.Status.PendingAuditEvents
	patch := client.MergeFromWithOptions(gate.DeepCopy(), client.MergeFromWithOptimisticLock{})
	gate.Status.Approvals = records
	// Audit each decision that appeared or left (an Approval created, or
	// deleted to revoke it). The records go in the audit outbox in the same
	// patch (#1552).
	enqueue := func(rec kardinalv1alpha1.GateApprovalStatus, action string) {
		if e, ok := r.approvalAuditEntry(gate, rec, action); ok {
			gate.Status.PendingAuditEvents = audit.Enqueue(ctx, auditKind, gate.Status.PendingAuditEvents, e)
		}
	}
	for _, rec := range records {
		if !hasDecision(old, rec) {
			enqueue(rec, auditActionApprovalRecorded)
		}
	}
	for _, o := range old {
		if !hasDecision(records, o) {
			enqueue(o, auditActionApprovalRevoked)
		}
	}
	if err := r.Status().Patch(ctx, gate, patch); err != nil {
		gate.Status.Approvals, gate.Status.PendingAuditEvents = old, oldPending
		return fmt.Errorf("record approvals: %w", err)
	}
	_ = r.flushAudit(ctx, gate)
	return nil
}

// Approval AuditEvent actions (#1449).
const (
	auditActionApprovalRecorded = "ApprovalRecorded"
	auditActionApprovalRevoked  = "ApprovalRevoked"
)

func hasDecision(list []kardinalv1alpha1.GateApprovalStatus, rec kardinalv1alpha1.GateApprovalStatus) bool {
	for _, o := range list {
		if o.User == rec.User && o.Decision == rec.Decision {
			return true
		}
	}
	return false
}

// approvalAuditEntry is the outbox entry recording that rec appeared in
// (ApprovalRecorded) or left (ApprovalRevoked) the gate's approvals, and
// false for a gate a Graph did not create. The name is derived from the gate,
// the action, the user, the decision and when the gate first saw it.
func (r *Reconciler) approvalAuditEntry(gate *kardinalv1alpha1.PolicyGate,
	rec kardinalv1alpha1.GateApprovalStatus, action string) (kardinalv1alpha1.PendingAuditEvent, bool) {
	labels := gate.GetLabels()
	if labels[labelPipeline] == "" || labels[labelBundle] == "" || labels[labelEnvironment] == "" {
		return kardinalv1alpha1.PendingAuditEvent{}, false
	}
	seen := ""
	if rec.FirstSeenAt != nil {
		seen = rec.FirstSeenAt.UTC().Format(time.RFC3339)
	}
	sum := sha256.Sum256([]byte(action + "\x00" + rec.User + "\x00" + rec.Decision + "\x00" + seen))
	suffix := "-approval-" + hex.EncodeToString(sum[:])[:12]
	base := gate.Name
	if limit := maxObjectNameLength - len(suffix); len(base) > limit {
		base = base[:limit]
	}
	counted := "counted"
	if !rec.Counted {
		counted = "not counted: " + rec.Reason
	}
	verb := map[string]string{auditActionApprovalRecorded: "recorded", auditActionApprovalRevoked: "revoked"}[action]
	msg := fmt.Sprintf("%s by %s %s on gate %s (%s)", rec.Decision, rec.User, verb, gate.Name, counted)
	if rec.Comment != "" {
		msg += ": " + rec.Comment
	}
	return audit.Entry(sanitizeGateName(base+suffix), gateAuditLabels(labels, action), kardinalv1alpha1.AuditEventSpec{
		BundleName: labels[labelBundle], PipelineName: labels[labelPipeline],
		Environment: labels[labelEnvironment], Action: action, Outcome: "Success", Message: msg, Actor: rec.Actor,
	}, metav1.NewTime(r.now())), true
}
