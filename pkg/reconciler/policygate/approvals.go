// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
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
// Bundle and environment (lifecycle.TallyApprovals) for a Bundle by author:
// the shared rules, so what the CLI says about who may approve matches what
// this counts.
func tallyApprovals(gate *kardinalv1alpha1.PolicyGate, author kardinalv1alpha1.Actor) approvalTally {
	return approvalTally{lifecycle.TallyApprovals(gate, author)}
}

// context is the approvals CEL variable: approvals.count (counted parties),
// approvals.required, approvals.users, approvals.rejected, approvals.met
// (the policy is met, or approval.requiredWhen waives it) and
// approvals.fields.<name>, the values the counted approvals give for each
// declared approval field (a list, empty when none gave it; a name the gate
// does not declare is an error, so the gate fails closed).
func (t approvalTally) context(met bool) map[string]interface{} {
	users := make([]interface{}, len(t.Approvers))
	for i, u := range t.Approvers {
		users[i] = u
	}
	fields := map[string]interface{}{}
	for name, values := range t.Fields {
		list := make([]interface{}, len(values))
		for i, v := range values {
			list[i] = v
		}
		fields[name] = list
	}
	return map[string]interface{}{
		"count":    int64(len(t.Approvers)),
		"required": int64(t.Required),
		"users":    users,
		"rejected": len(t.Rejecters) > 0,
		"met":      met,
		"fields":   fields,
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
func (r *Reconciler) recordApprovals(ctx context.Context, gate *kardinalv1alpha1.PolicyGate, t approvalTally, bundleUID string) error {
	now := metav1.NewTime(r.now())
	records := make([]kardinalv1alpha1.GateApprovalStatus, len(t.Records))
	for i, rec := range t.Records {
		rec.FirstSeenAt = &now
		for _, old := range gate.Status.Approvals {
			if sameDecision(old, rec) && old.FirstSeenAt != nil {
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
		if e, ok := r.approvalAuditEntry(gate, rec, action, bundleUID); ok {
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
		if sameDecision(o, rec) {
			return true
		}
	}
	return false
}

// sameDecision reports whether a and b record one decision: the same user,
// decision and fields. An Approval replaced with other fields is a new
// decision, recorded and audited anew.
func sameDecision(a, b kardinalv1alpha1.GateApprovalStatus) bool {
	return a.User == b.User && a.Decision == b.Decision && equality.Semantic.DeepEqual(a.Fields, b.Fields)
}

// approvalAuditEntry is the outbox entry recording that rec appeared in
// (ApprovalRecorded) or left (ApprovalRevoked) the gate's approvals, and
// false for a gate a Graph did not create. The name is derived from the gate,
// the action, the user, the decision, when the gate first saw it and the
// Bundle's UID (#2380: a Bundle created again under its name gets records of
// its own).
func (r *Reconciler) approvalAuditEntry(gate *kardinalv1alpha1.PolicyGate,
	rec kardinalv1alpha1.GateApprovalStatus, action, bundleUID string) (kardinalv1alpha1.PendingAuditEvent, bool) {
	labels := gate.GetLabels()
	if labels[labelPipeline] == "" || labels[labelBundle] == "" || labels[labelEnvironment] == "" || bundleUID == "" {
		return kardinalv1alpha1.PendingAuditEvent{}, false
	}
	seen := ""
	if rec.FirstSeenAt != nil {
		seen = rec.FirstSeenAt.UTC().Format(time.RFC3339)
	}
	// The fields are part of the decision: an Approval replaced with other
	// fields within the same second is another record.
	sum := sha256.Sum256([]byte(action + "\x00" + rec.User + "\x00" + rec.Decision + "\x00" + seen + fieldsKey(rec.Fields) +
		"\x00" + bundleUID))
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
	if s := fieldsSummary(rec.Fields); s != "" {
		msg += "; fields: " + s
	}
	return audit.Entry(sanitizeGateName(base+suffix), gateAuditLabels(labels, action), kardinalv1alpha1.AuditEventSpec{
		BundleName: labels[labelBundle], BundleUID: bundleUID, PipelineName: labels[labelPipeline],
		Environment: labels[labelEnvironment], Action: action, Outcome: "Success", Message: msg, Actor: rec.Actor,
		Fields: wholeFields(gate, rec),
	}, metav1.NewTime(r.now())), true
}

// fieldsSummary is fields as "a=x, b=y", sorted by name (values as
// status.approvals keeps them, cut to MaxRecordedFieldValue).
func fieldsSummary(fields map[string]string) string {
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + "=" + fields[n]
	}
	return strings.Join(parts, ", ")
}

// wholeFields is rec's declared fields as the decision gave them, whole,
// for the AuditEvent's structured field: from the gate's spec.approvals
// (status.approvals keeps them cut).
func wholeFields(gate *kardinalv1alpha1.PolicyGate, rec kardinalv1alpha1.GateApprovalStatus) map[string]string {
	if len(rec.Fields) == 0 {
		return nil
	}
	for _, a := range gate.Spec.Approvals {
		if a.User == rec.User && a.Decision == rec.Decision &&
			equality.Semantic.DeepEqual(lifecycle.DeclaredFields(gate.Spec.Approval, a.Fields, lifecycle.MaxRecordedFieldValue), rec.Fields) {
			if whole := lifecycle.DeclaredFields(gate.Spec.Approval, a.Fields, 0); whole != nil {
				return whole
			}
		}
	}
	return rec.Fields
}

// fieldsKey is fields in a stable form for a record's name: "" without any,
// so records without fields keep the names they had before fields existed.
func fieldsKey(fields map[string]string) string {
	if len(fields) == 0 {
		return ""
	}
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString("\x00" + n + "=" + fields[n])
	}
	return b.String()
}
