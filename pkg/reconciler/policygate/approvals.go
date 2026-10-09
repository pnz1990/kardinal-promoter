// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
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
}

// tallyApprovals counts the decisions of gate's spec.approvals for its
// Bundle and environment. A decision counts when its user is in
// allowedUsers or one of its groups is in allowedGroups (everyone counts
// when both are empty), and, with excludeAuthor, its user is not author.
// Each user counts once; a reject from a counted user wins over their
// approve. Decisions for another Bundle or environment are ignored: the
// Graph filters them, so they only come from a hand-edited spec.
func tallyApprovals(gate *kardinalv1alpha1.PolicyGate, author string) approvalTally {
	t := approvalTally{comments: map[string]string{}}
	p := gate.Spec.Approval
	if p != nil {
		t.required = max(p.Required, 1)
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
		case p != nil && p.ExcludeAuthor && author != "" && a.User == author:
			rec.Reason = "the Bundle's author (approval.excludeAuthor)"
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
	patch := client.MergeFrom(gate.DeepCopy())
	gate.Status.Approvals = records
	if err := r.Status().Patch(ctx, gate, patch); err != nil {
		return fmt.Errorf("record approvals: %w", err)
	}
	return nil
}
