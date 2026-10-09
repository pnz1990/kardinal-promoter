// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// approvalTally is how a gate instance counts the decisions in its
// spec.approvals (copied by the Graph from Approval objects) against its
// spec.approval policy.
type approvalTally struct {
	// required is spec.approval.required, 0 without a policy.
	required int
	// approvers are the distinct users whose approve counts, sorted.
	approvers []string
	// rejecters are the distinct users whose reject counts, sorted, with
	// their comment.
	rejecters []string
	comments  map[string]string
	// records is one entry per decision, for status.approvals.
	records []kardinalv1alpha1.GateApprovalStatus
	// held is a reason the policy cannot be met at all: too many Approvals,
	// or excludeAuthor on a Bundle with no verified creator.
	held string
}

// maxGateApprovals is how many decisions a gate counts; the Graph copies at
// most one more, so a gate with more blocks instead of counting a cut list.
const maxGateApprovals = 100

// tallyApprovals counts the decisions of gate's spec.approvals for its
// Bundle and environment. A decision counts when its user is in
// allowedUsers or one of its groups is in allowedGroups (everyone counts
// when both are empty), and, with excludeAuthor, its user is not creator, the
// Bundle's verified kardinal.io/created-by; with no creator, excludeAuthor
// holds the gate.
// Each user counts once; a reject from a counted user wins over their
// approve. Decisions for another Bundle or environment are ignored: the
// Graph filters them, so they only come from a hand-edited spec.
func tallyApprovals(gate *kardinalv1alpha1.PolicyGate, creator string) approvalTally {
	t := approvalTally{comments: map[string]string{}}
	p := gate.Spec.Approval
	if p != nil {
		t.required = max(p.Required, 1)
		switch {
		case len(gate.Spec.Approvals) > maxGateApprovals:
			t.held = fmt.Sprintf("more than %d Approvals for this Bundle in %s; delete the stale ones (kubectl get approvals)",
				maxGateApprovals, gate.Labels[labelEnvironment])
		case p.ExcludeAuthor && creator == "":
			t.held = "excludeAuthor cannot be enforced: the Bundle has no verified creator (annotation kardinal.io/created-by)"
		case p.ExcludeAuthor && lifecycle.ComponentCreator(creator):
			t.held = fmt.Sprintf("excludeAuthor cannot be enforced: the Bundle was created by the kardinal component %q, "+
				"not a person; create it as yourself (kardinal create bundle, or the Bundle API with your own token)", creator)
		}
	}
	bundle, env := gate.Labels[labelBundle], gate.Labels[labelEnvironment]
	approved, rejected := map[string]bool{}, map[string]bool{}
	for _, a := range gate.Spec.Approvals {
		rec := kardinalv1alpha1.GateApprovalStatus{User: a.User, Decision: a.Decision, Comment: a.Comment}
		switch {
		case a.Bundle != bundle || a.Environment != env:
			rec.Reason = fmt.Sprintf("for bundle %s in %s, not this gate's", a.Bundle, a.Environment)
		case p != nil && !allowed(p, a):
			rec.Reason = "not an allowed approver (approval.allowedUsers, approval.allowedGroups)"
		case p != nil && p.ExcludeAuthor && creator != "" && a.User == creator:
			rec.Reason = "the Bundle's creator (approval.excludeAuthor)"
		default:
			rec.Counted = true
			if a.Decision == "reject" {
				rejected[a.User] = true
				if a.Comment != "" {
					t.comments[a.User] = a.Comment
				}
			} else {
				approved[a.User] = true
			}
		}
		t.records = append(t.records, rec)
	}
	for u := range rejected {
		t.rejecters = append(t.rejecters, u)
	}
	for u := range approved {
		if !rejected[u] {
			t.approvers = append(t.approvers, u)
		}
	}
	sort.Strings(t.rejecters)
	sort.Strings(t.approvers)
	sort.SliceStable(t.records, func(i, j int) bool { return t.records[i].User < t.records[j].User })
	return t
}

func allowed(p *kardinalv1alpha1.GateApprovalPolicy, a kardinalv1alpha1.GateApproval) bool {
	if len(p.AllowedUsers) == 0 && len(p.AllowedGroups) == 0 {
		return true
	}
	if slices.Contains(p.AllowedUsers, a.User) {
		return true
	}
	for _, g := range a.Groups {
		if slices.Contains(p.AllowedGroups, g) {
			return true
		}
	}
	return false
}

// context is the approvals CEL variable: approvals.count (counted approvers),
// approvals.required, approvals.users and approvals.rejected.
func (t approvalTally) context() map[string]interface{} {
	users := make([]interface{}, len(t.approvers))
	for i, u := range t.approvers {
		users[i] = u
	}
	return map[string]interface{}{
		"count":    int64(len(t.approvers)),
		"required": int64(t.required),
		"users":    users,
		"rejected": len(t.rejecters) > 0,
	}
}

// blocked returns why the approval policy holds the gate, or "" when it is
// met or the gate has no policy.
func (t approvalTally) blocked() string {
	if t.required == 0 {
		return ""
	}
	if t.held != "" {
		return t.held
	}
	if len(t.rejecters) > 0 {
		var parts []string
		for _, u := range t.rejecters {
			if c := t.comments[u]; c != "" {
				parts = append(parts, fmt.Sprintf("%s (%s)", u, c))
			} else {
				parts = append(parts, u)
			}
		}
		return "rejected by " + strings.Join(parts, ", ")
	}
	if len(t.approvers) < t.required {
		msg := fmt.Sprintf("waiting for approvals: %d of %d", len(t.approvers), t.required)
		if len(t.approvers) > 0 {
			msg += " (" + strings.Join(t.approvers, ", ") + ")"
		}
		return msg
	}
	return ""
}

// met returns the reason suffix of a gate whose policy is met.
func (t approvalTally) met() string {
	if t.required == 0 {
		return ""
	}
	return fmt.Sprintf("approved by %s (%d of %d)", strings.Join(t.approvers, ", "), len(t.approvers), t.required)
}

// recordApprovals writes the tally's records to status.approvals when they
// changed. firstSeenAt is kept for a decision already recorded (same user and
// decision) and set to now for a new one. Graph-first: the gate's own status,
// from its own spec.
func (r *Reconciler) recordApprovals(ctx context.Context, gate *kardinalv1alpha1.PolicyGate, t approvalTally) error {
	now := metav1.NewTime(r.now())
	records := make([]kardinalv1alpha1.GateApprovalStatus, len(t.records))
	for i, rec := range t.records {
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
	old := gate.Status.Approvals
	patch := client.MergeFromWithOptions(gate.DeepCopy(), client.MergeFromWithOptimisticLock{})
	gate.Status.Approvals = records
	if err := r.Status().Patch(ctx, gate, patch); err != nil {
		gate.Status.Approvals = old
		return fmt.Errorf("record approvals: %w", err)
	}
	// Audit each decision that appeared or left (an Approval created, or
	// deleted to revoke it).
	for _, rec := range records {
		if !hasDecision(old, rec) {
			r.writeApprovalAuditEvent(ctx, gate, rec, auditActionApprovalRecorded)
		}
	}
	for _, o := range old {
		if !hasDecision(records, o) {
			r.writeApprovalAuditEvent(ctx, gate, o, auditActionApprovalRevoked)
		}
	}
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

// writeApprovalAuditEvent records that rec appeared in (ApprovalRecorded) or
// left (ApprovalRevoked) the gate's approvals. The name is derived from the
// gate, the action, the user, the decision and when the gate first saw it. A
// failure is logged: audit never blocks gate evaluation.
func (r *Reconciler) writeApprovalAuditEvent(ctx context.Context, gate *kardinalv1alpha1.PolicyGate,
	rec kardinalv1alpha1.GateApprovalStatus, action string) {
	labels := gate.GetLabels()
	if labels[labelPipeline] == "" || labels[labelBundle] == "" || labels[labelEnvironment] == "" {
		return
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
	ae := &kardinalv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{Name: sanitizeGateName(base + suffix), Namespace: gate.Namespace,
			Labels: gateAuditLabels(labels, action)},
		Spec: kardinalv1alpha1.AuditEventSpec{
			Timestamp: metav1.NewTime(r.now()), BundleName: labels[labelBundle], PipelineName: labels[labelPipeline],
			Environment: labels[labelEnvironment], Action: action, Outcome: "Success", Message: truncateMessage(msg),
		},
	}
	if err := r.Create(ctx, ae); client.IgnoreAlreadyExists(err) != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Str("gate", gate.Name).Str("action", action).Msg("failed to write approval AuditEvent")
	}
}
