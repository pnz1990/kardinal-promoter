// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lifecycle

import (
	"fmt"
	"slices"
	"sort"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// MaxGateApprovals is how many decisions a gate counts; the Graph copies at
// most one more, so a gate with more blocks instead of counting a cut list.
const MaxGateApprovals = 100

// ApprovalTally is how a gate instance counts the decisions in its
// spec.approvals (copied by the Graph from Approval objects) against its
// spec.approval policy. The PolicyGate reconciler decides with it, and the
// CLI and the UI API say who may approve from it (ApprovalStandingOf), so
// they cannot disagree with the server.
type ApprovalTally struct {
	// Required is spec.approval.required (at least 1), 0 without a policy.
	Required int
	// Approvers are the distinct users whose approve counts, sorted.
	Approvers []string
	// Rejecters are the distinct users whose reject counts, sorted; Comments
	// holds their comments.
	Rejecters []string
	Comments  map[string]string
	// Records is one entry per decision, for status.approvals.
	Records []kardinalv1alpha1.GateApprovalStatus
	// Held is why the policy cannot be met at all: too many Approvals, or
	// excludeAuthor on a Bundle with no verified creator or one a kardinal
	// component created.
	Held string
}

// ApprovalBlock is why an approval does not count, or cannot be given, as a
// value to branch on; ApprovalStanding.Reason says it in words.
type ApprovalBlock string

const (
	// ApprovalOpen: nothing blocks (the caller may approve, or is unknown).
	ApprovalOpen ApprovalBlock = ""
	// ApprovalTooMany: more than MaxGateApprovals decisions; the policy
	// cannot be met (held).
	ApprovalTooMany ApprovalBlock = "TooMany"
	// ApprovalNoCreator: excludeAuthor, and the Bundle has no verified
	// creator (held).
	ApprovalNoCreator ApprovalBlock = "NoCreator"
	// ApprovalComponentCreator: excludeAuthor, and a kardinal component
	// created the Bundle (held).
	ApprovalComponentCreator ApprovalBlock = "ComponentCreator"
	// ApprovalRejected: a counted reject blocks the gate.
	ApprovalRejected ApprovalBlock = "Rejected"
	// ApprovalCounted: the caller's approve already counts.
	ApprovalCounted ApprovalBlock = "Counted"
	// ApprovalNotAllowed: the caller is not in allowedUsers or allowedGroups.
	ApprovalNotAllowed ApprovalBlock = "NotAllowed"
	// ApprovalAuthor: excludeAuthor, and the caller created the Bundle.
	ApprovalAuthor ApprovalBlock = "Author"
)

// Held reports whether b means the policy cannot be met at all, whatever
// approvals come.
func (b ApprovalBlock) Held() bool {
	return b == ApprovalTooMany || b == ApprovalNoCreator || b == ApprovalComponentCreator
}

// approvalPolicyBlock is why approval policy p of a gate with n decisions
// cannot be met for a Bundle created by creator: a held ApprovalBlock, or
// ApprovalOpen.
func approvalPolicyBlock(p *kardinalv1alpha1.GateApprovalPolicy, n int, creator string) ApprovalBlock {
	switch {
	case p == nil:
		return ApprovalOpen
	case n > MaxGateApprovals:
		return ApprovalTooMany
	case p.ExcludeAuthor && creator == "":
		return ApprovalNoCreator
	case p.ExcludeAuthor && ComponentCreator(creator):
		return ApprovalComponentCreator
	}
	return ApprovalOpen
}

// ApprovalPolicyHeld is why approval policy p of a gate with n decisions
// cannot be met for a Bundle created by creator (its verified
// kardinal.io/created-by), or "".
func ApprovalPolicyHeld(p *kardinalv1alpha1.GateApprovalPolicy, n int, creator, env string) string {
	switch approvalPolicyBlock(p, n, creator) {
	case ApprovalTooMany:
		return fmt.Sprintf("more than %d Approvals for this Bundle in %s; delete the stale ones (kubectl get approvals)", MaxGateApprovals, env)
	case ApprovalNoCreator:
		return "excludeAuthor cannot be enforced: the Bundle has no verified creator (annotation kardinal.io/created-by)"
	case ApprovalComponentCreator:
		return fmt.Sprintf("excludeAuthor cannot be enforced: the Bundle was created by the kardinal component %q, "+
			"not a person; create it as yourself (kardinal create bundle, or the Bundle API with your own token)", creator)
	}
	return ""
}

// approvalDecisionBlock is why a decision by user (in groups) does not
// count under approval policy p for a Bundle created by creator, or
// ApprovalOpen.
func approvalDecisionBlock(p *kardinalv1alpha1.GateApprovalPolicy, user string, groups []string, creator string) ApprovalBlock {
	switch {
	case p == nil:
		return ApprovalOpen
	case !approvalAllowed(p, user, groups):
		return ApprovalNotAllowed
	case p.ExcludeAuthor && creator != "" && user == creator:
		return ApprovalAuthor
	}
	return ApprovalOpen
}

// ApprovalDecisionCounts reports whether a decision by user (in groups)
// counts under approval policy p for a Bundle created by creator, and why
// not: the user is in allowedUsers or one of the groups in allowedGroups
// (anyone when both are empty), and, with excludeAuthor, is not creator.
func ApprovalDecisionCounts(p *kardinalv1alpha1.GateApprovalPolicy, user string, groups []string, creator string) (bool, string) {
	switch approvalDecisionBlock(p, user, groups, creator) {
	case ApprovalNotAllowed:
		return false, "not an allowed approver (approval.allowedUsers, approval.allowedGroups)"
	case ApprovalAuthor:
		return false, "the version's author (approval.excludeAuthor)"
	}
	return true, ""
}

func approvalAllowed(p *kardinalv1alpha1.GateApprovalPolicy, user string, groups []string) bool {
	if len(p.AllowedUsers) == 0 && len(p.AllowedGroups) == 0 {
		return true
	}
	if slices.Contains(p.AllowedUsers, user) {
		return true
	}
	for _, g := range groups {
		if slices.Contains(p.AllowedGroups, g) {
			return true
		}
	}
	return false
}

// TallyApprovals counts the decisions of gate's spec.approvals for its
// Bundle and environment (labels kardinal.io/bundle, kardinal.io/environment)
// with ApprovalDecisionCounts. Each user counts once; a reject from a counted
// user wins over their approve. Decisions for another Bundle or environment
// are ignored: the Graph filters them, so they only come from a hand-edited
// spec.
func TallyApprovals(gate *kardinalv1alpha1.PolicyGate, creator string) ApprovalTally {
	t := ApprovalTally{Comments: map[string]string{}}
	p := gate.Spec.Approval
	bundle, env := gate.Labels["kardinal.io/bundle"], gate.Labels["kardinal.io/environment"]
	if p != nil {
		t.Required = max(p.Required, 1)
		t.Held = ApprovalPolicyHeld(p, len(gate.Spec.Approvals), creator, env)
	}
	approved, rejected := map[string]bool{}, map[string]bool{}
	for _, a := range gate.Spec.Approvals {
		rec := kardinalv1alpha1.GateApprovalStatus{User: a.User, Decision: a.Decision, Comment: a.Comment, Actor: a.Actor}
		counts, why := ApprovalDecisionCounts(p, a.User, a.Groups, creator)
		switch {
		case a.Bundle != bundle || a.Environment != env:
			rec.Reason = fmt.Sprintf("for bundle %s in %s, not this gate's", a.Bundle, a.Environment)
		case !counts:
			rec.Reason = why
		default:
			rec.Counted = true
			if a.Decision == "reject" {
				rejected[a.User] = true
				if a.Comment != "" {
					t.Comments[a.User] = a.Comment
				}
			} else {
				approved[a.User] = true
			}
		}
		t.Records = append(t.Records, rec)
	}
	for u := range rejected {
		t.Rejecters = append(t.Rejecters, u)
	}
	for u := range approved {
		if !rejected[u] {
			t.Approvers = append(t.Approvers, u)
		}
	}
	sort.Strings(t.Rejecters)
	sort.Strings(t.Approvers)
	sort.SliceStable(t.Records, func(i, j int) bool { return t.Records[i].User < t.Records[j].User })
	return t
}

// ApproverIdentity is who asks: a Kubernetes user and their groups.
type ApproverIdentity struct {
	User   string
	Groups []string
}

// ApprovalStanding is where a caller stands with an approval gate, from the
// server's own tally (TallyApprovals).
type ApprovalStanding struct {
	// Approved and Required are the counted approvals and the quorum.
	Approved, Required int
	// CanApprove is true when the caller's approval would count and still
	// matters: the policy can be met, nobody rejected it, the caller may
	// approve and has not yet.
	CanApprove bool
	// AlreadyCounted is true when the caller's approve already counts.
	AlreadyCounted bool
	// RejectedBy are the users whose reject counts (the gate is blocked).
	RejectedBy []string
	// Block is why the caller cannot approve, to branch on: held first (the
	// policy cannot be met), then rejected, then the caller's own (counted,
	// not allowed, the author). ApprovalOpen when CanApprove, or when the
	// caller is unknown and nothing else blocks.
	Block ApprovalBlock
	// Reason is Block in words ("" for ApprovalOpen).
	Reason string
	// Who may approve when the caller cannot: allowedUsers and "group <g>"
	// of allowedGroups; empty when anyone with RBAC may.
	Who []string
}

// ApprovalStandingOf is where id stands with approval gate gate of a Bundle
// created by creator. A nil id (the caller is unknown) gets the tally and
// Who, with CanApprove false.
func ApprovalStandingOf(gate *kardinalv1alpha1.PolicyGate, creator string, id *ApproverIdentity) ApprovalStanding {
	t := TallyApprovals(gate, creator)
	s := ApprovalStanding{Approved: len(t.Approvers), Required: t.Required, RejectedBy: t.Rejecters}
	if p := gate.Spec.Approval; p != nil {
		s.Who = append(s.Who, p.AllowedUsers...)
		for _, g := range p.AllowedGroups {
			s.Who = append(s.Who, "group "+g)
		}
	}
	if id != nil {
		s.AlreadyCounted = slices.Contains(t.Approvers, id.User)
	}
	p := gate.Spec.Approval
	switch {
	case t.Held != "":
		s.Block, s.Reason = approvalPolicyBlock(p, len(gate.Spec.Approvals), creator), t.Held
	case len(t.Rejecters) > 0:
		s.Block, s.Reason = ApprovalRejected, "rejected by "+joinUsers(t.Rejecters)
	case id == nil:
	case s.AlreadyCounted:
		s.Block, s.Reason = ApprovalCounted, "you approved it; it needs someone else"
	default:
		s.Block = approvalDecisionBlock(p, id.User, id.Groups, creator)
		ok, why := ApprovalDecisionCounts(p, id.User, id.Groups, creator)
		s.CanApprove, s.Reason = ok, why
	}
	return s
}

// joinUsers is users as "a, b and c".
func joinUsers(users []string) string {
	switch len(users) {
	case 0:
		return ""
	case 1:
		return users[0]
	}
	out := ""
	for i, u := range users[:len(users)-1] {
		if i > 0 {
			out += ", "
		}
		out += u
	}
	return out + " and " + users[len(users)-1]
}
