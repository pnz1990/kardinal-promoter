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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// MaxGateApprovals is how many decisions a gate counts; the Graph copies at
// most one more, so a gate with more blocks instead of counting a cut list.
const MaxGateApprovals = 100

// ApprovalTally is how a gate instance counts the decisions in its
// spec.approvals (copied by the Graph from Approval objects) against its
// spec.approval policy. The PolicyGate reconciler decides with it, and the
// CLI and the UI API say who may approve from it (ApprovalStandingOf), so
// they cannot disagree with the server.
//
// Decisions are counted per party (docs/design/18-actor-policy.md): a
// person, or an agent together with its sponsor (actor.PartyKey). Alice and
// her agent are one approval.
type ApprovalTally struct {
	// Required is spec.approval.required (at least 1), 0 without a policy.
	Required int
	// Approvers are the parties whose approve counts, one name each (the
	// first approver of the party by name), sorted.
	Approvers []string
	// Rejecters are the parties whose reject counts, one name each, sorted;
	// Comments holds their comments.
	Rejecters []string
	Comments  map[string]string
	// Records is one entry per decision, for status.approvals.
	Records []kardinalv1alpha1.GateApprovalStatus
	// Held is why the policy cannot be met at all: too many Approvals, or
	// excludeAuthor on a Bundle with no verified creator or one a kardinal
	// component created.
	Held string
	// countedKeys maps the party key of each of Approvers to its name.
	countedKeys map[string]string
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
	// ApprovalCounted: the caller's party already counts.
	ApprovalCounted ApprovalBlock = "Counted"
	// ApprovalNotAllowed: the caller is not in allowedUsers or allowedGroups.
	ApprovalNotAllowed ApprovalBlock = "NotAllowed"
	// ApprovalAuthor: excludeAuthor, and the caller is of the author's party
	// (the author, the author's agent or sponsor, or a sibling agent).
	ApprovalAuthor ApprovalBlock = "Author"
	// ApprovalAgentsOff: the caller is an agent and the gate does not opt
	// in to agent approvals (allowAgents, allowedAgents).
	ApprovalAgentsOff ApprovalBlock = "AgentsOff"
	// ApprovalAgentNotAllowed: the caller is an agent the gate does not list
	// in allowedAgents.
	ApprovalAgentNotAllowed ApprovalBlock = "AgentNotAllowed"
	// ApprovalOwnParty: the caller is an agent of the author's party; an
	// agent never counts on its own party's work.
	ApprovalOwnParty ApprovalBlock = "OwnParty"
)

// Held reports whether b means the policy cannot be met at all, whatever
// approvals come.
func (b ApprovalBlock) Held() bool {
	return b == ApprovalTooMany || b == ApprovalNoCreator || b == ApprovalComponentCreator
}

// BundleAuthor is who created b, for the approval rules: its actor stamp
// (spec.actor), or for a Bundle without one {kind: unknown, principal:
// kardinal.io/created-by} (docs/design/17-actors.md section 4.3).
func BundleAuthor(b *kardinalv1alpha1.Bundle) kardinalv1alpha1.Actor {
	if b == nil {
		return actor.OrUnknown(nil, "")
	}
	return actor.OrUnknown(b.Spec.Actor, b.Annotations[AnnotationCreatedBy])
}

// componentAuthor reports whether author is a kardinal component rather
// than a person or an agent: a controller stamp, or a component creator
// name (subscription:*, bundle-api, kardinal-ui, kardinal-controller).
func componentAuthor(author kardinalv1alpha1.Actor) bool {
	return author.Kind == kardinalv1alpha1.ActorController || ComponentCreator(author.Principal)
}

// approvalPolicyBlock is why approval policy p of a gate with n decisions
// cannot be met for a Bundle by author: a held ApprovalBlock, or
// ApprovalOpen.
func approvalPolicyBlock(p *kardinalv1alpha1.GateApprovalPolicy, n int, author kardinalv1alpha1.Actor) ApprovalBlock {
	switch {
	case p == nil:
		return ApprovalOpen
	case n > MaxGateApprovals:
		return ApprovalTooMany
	case p.ExcludeAuthor && author.Principal == "":
		return ApprovalNoCreator
	case p.ExcludeAuthor && componentAuthor(author):
		return ApprovalComponentCreator
	}
	return ApprovalOpen
}

// ApprovalPolicyHeld is why approval policy p of a gate with n decisions
// cannot be met for a Bundle by author (BundleAuthor), or "".
func ApprovalPolicyHeld(p *kardinalv1alpha1.GateApprovalPolicy, n int, author kardinalv1alpha1.Actor, env string) string {
	switch approvalPolicyBlock(p, n, author) {
	case ApprovalTooMany:
		return fmt.Sprintf("more than %d Approvals for this Bundle in %s; delete the stale ones (kubectl get approvals)", MaxGateApprovals, env)
	case ApprovalNoCreator:
		return "excludeAuthor cannot be enforced: the Bundle has no verified creator (annotation kardinal.io/created-by)"
	case ApprovalComponentCreator:
		return fmt.Sprintf("excludeAuthor cannot be enforced: the Bundle was created by the kardinal component %q, "+
			"not a person; create it as yourself (kardinal create bundle, or the Bundle API with your own token)", author.Principal)
	}
	return ""
}

// Approver is who decides: the Approval's user and groups, and its actor
// stamp (nil without one).
type Approver struct {
	User   string
	Groups []string
	Actor  *kardinalv1alpha1.Actor
}

// actorOf is a's actor: its stamp, or {kind: unknown, principal: user}.
func (a Approver) actorOf() kardinalv1alpha1.Actor {
	return actor.OrUnknown(a.Actor, a.User)
}

// approvalDecisionBlock is why a decision by a does not count under approval
// policy p for a Bundle by author, or ApprovalOpen.
func approvalDecisionBlock(p *kardinalv1alpha1.GateApprovalPolicy, a Approver, author kardinalv1alpha1.Actor) ApprovalBlock {
	who := a.actorOf()
	if who.Kind == kardinalv1alpha1.ActorAgent {
		// Agent approvals are opt-in, by name, and never count on the
		// agent's own party's work (18-actor-policy section 5).
		switch {
		case p == nil || !p.AllowAgents:
			return ApprovalAgentsOff
		case !slices.Contains(p.AllowedAgents, who.Agent):
			return ApprovalAgentNotAllowed
		case author.Principal != "" && actor.SameParty(author, who):
			return ApprovalOwnParty
		}
		return ApprovalOpen
	}
	switch {
	case p == nil:
		return ApprovalOpen
	case !approvalAllowed(p, a.User, a.Groups):
		return ApprovalNotAllowed
	case p.ExcludeAuthor && author.Principal != "" && actor.SameParty(author, who):
		return ApprovalAuthor
	}
	return ApprovalOpen
}

// partyRelation names how approver belongs to author's party, for reasons.
func partyRelation(author, approver kardinalv1alpha1.Actor) string {
	agentAuthor := author.Kind == kardinalv1alpha1.ActorAgent
	agentApprover := approver.Kind == kardinalv1alpha1.ActorAgent
	switch {
	case approver.Principal == author.Principal:
		return "the version's author"
	case agentAuthor && agentApprover && approver.Agent == author.Agent:
		return "the version's agent"
	case agentAuthor && approver.Principal == author.Sponsor:
		return "the sponsor of the version's agent " + author.Agent
	case agentApprover && approver.Sponsor == author.Principal:
		return "the author's own agent " + approver.Agent
	case agentAuthor && agentApprover:
		return "an agent of the same sponsor as the version's agent " + author.Agent
	}
	return "the author's party"
}

// ApprovalDecisionCounts reports whether a decision by a counts under
// approval policy p for a Bundle by author, and why not:
//   - a person counts when in allowedUsers or one of allowedGroups (anyone
//     when both are empty) and, with excludeAuthor, not of the author's party;
//   - an agent counts only when the gate opts in (allowAgents and the agent
//     in allowedAgents), and never on its own party's work.
func ApprovalDecisionCounts(p *kardinalv1alpha1.GateApprovalPolicy, a Approver, author kardinalv1alpha1.Actor) (bool, string) {
	switch approvalDecisionBlock(p, a, author) {
	case ApprovalNotAllowed:
		return false, "not an allowed approver (approval.allowedUsers, approval.allowedGroups)"
	case ApprovalAuthor:
		return false, partyRelation(author, a.actorOf()) + " (approval.excludeAuthor)"
	case ApprovalAgentsOff:
		return false, "an agent; this gate does not count agent approvals (approval.allowAgents)"
	case ApprovalAgentNotAllowed:
		return false, fmt.Sprintf("agent %s is not in approval.allowedAgents", a.actorOf().Agent)
	case ApprovalOwnParty:
		return false, partyRelation(author, a.actorOf()) + " (an agent never counts on its own party's work)"
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
// with ApprovalDecisionCounts, for a Bundle by author (BundleAuthor). Each
// party counts once (actor.PartyKey); a counted reject from a party wins
// over its approve. Decisions for another Bundle or environment are ignored:
// the Graph filters them, so they only come from a hand-edited spec.
func TallyApprovals(gate *kardinalv1alpha1.PolicyGate, author kardinalv1alpha1.Actor) ApprovalTally {
	t := ApprovalTally{Comments: map[string]string{}, countedKeys: map[string]string{}}
	p := gate.Spec.Approval
	bundle, env := gate.Labels["kardinal.io/bundle"], gate.Labels["kardinal.io/environment"]
	if p != nil {
		t.Required = max(p.Required, 1)
		t.Held = ApprovalPolicyHeld(p, len(gate.Spec.Approvals), author, env)
	}
	// Sorted by user, so a party's name is its first approver by name.
	decisions := slices.Clone(gate.Spec.Approvals)
	sort.SliceStable(decisions, func(i, j int) bool { return decisions[i].User < decisions[j].User })
	approved, rejected := map[string]string{}, map[string]string{}
	for _, a := range decisions {
		rec := kardinalv1alpha1.GateApprovalStatus{User: a.User, Decision: a.Decision, Comment: a.Comment, Actor: a.Actor}
		who := Approver{User: a.User, Groups: a.Groups, Actor: a.Actor}
		counts, why := ApprovalDecisionCounts(p, who, author)
		switch {
		case a.Bundle != bundle || a.Environment != env:
			rec.Reason = fmt.Sprintf("for bundle %s in %s, not this gate's", a.Bundle, a.Environment)
		case !counts:
			rec.Reason = why
		default:
			rec.Counted = true
			key := actor.PartyKey(who.actorOf())
			if a.Decision == "reject" {
				if _, ok := rejected[key]; !ok {
					rejected[key] = a.User
				}
				if a.Comment != "" {
					t.Comments[rejected[key]] = a.Comment
				}
			} else if _, ok := approved[key]; !ok {
				approved[key] = a.User
			} else if approved[key] != a.User {
				rec.Counted = false
				rec.Reason = fmt.Sprintf("already counted for %s (one vote per party)", approved[key])
			}
		}
		t.Records = append(t.Records, rec)
	}
	for _, u := range rejected {
		t.Rejecters = append(t.Rejecters, u)
	}
	for key, u := range approved {
		if _, ok := rejected[key]; !ok {
			t.Approvers = append(t.Approvers, u)
			t.countedKeys[key] = u
		}
	}
	sort.Strings(t.Rejecters)
	sort.Strings(t.Approvers)
	return t
}

// Met reports whether the approval policy is met: the gate has no policy,
// or enough parties approved, nobody rejected and nothing holds it.
func (t ApprovalTally) Met() bool {
	if t.Required == 0 {
		return true
	}
	return t.Held == "" && len(t.Rejecters) == 0 && len(t.Approvers) >= t.Required
}

// ApprovalWaived reports whether gate's last evaluation found its
// approval.requiredWhen false (status.approvalRequired): its policy does not
// hold it now, so it is an ordinary gate, not one waiting on an approval.
func ApprovalWaived(gate *kardinalv1alpha1.PolicyGate) bool {
	return gate.Status.ApprovalRequired != nil && !*gate.Status.ApprovalRequired
}

// ApproverIdentity is who asks: a Kubernetes user and their groups, and
// their actor when the caller derived it (nil: a person, as v0.11).
type ApproverIdentity struct {
	User   string
	Groups []string
	Actor  *kardinalv1alpha1.Actor
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
	// AlreadyCounted is true when the caller's party already counts.
	AlreadyCounted bool
	// RejectedBy are the users whose reject counts (the gate is blocked).
	RejectedBy []string
	// Block is why the caller cannot approve, to branch on: held first (the
	// policy cannot be met), then rejected, then the caller's own (counted,
	// not allowed, the author's party, agents). ApprovalOpen when
	// CanApprove, or when the caller is unknown and nothing else blocks.
	Block ApprovalBlock
	// Reason is Block in words ("" for ApprovalOpen).
	Reason string
	// Who may approve when the caller cannot: allowedUsers, "group <g>" of
	// allowedGroups and "agent <a>" of allowedAgents; empty when anyone with
	// RBAC may.
	Who []string
	// Waived is true when the gate's last evaluation found
	// approval.requiredWhen false: the policy does not hold it now, so it
	// is not waiting on an approval.
	Waived bool
}

// ApprovalStandingOf is where id stands with approval gate gate of a Bundle
// by author (BundleAuthor). A nil id (the caller is unknown) gets the tally
// and Who, with CanApprove false.
func ApprovalStandingOf(gate *kardinalv1alpha1.PolicyGate, author kardinalv1alpha1.Actor, id *ApproverIdentity) ApprovalStanding {
	t := TallyApprovals(gate, author)
	s := ApprovalStanding{Approved: len(t.Approvers), Required: t.Required, RejectedBy: t.Rejecters}
	s.Waived = ApprovalWaived(gate)
	p := gate.Spec.Approval
	if p != nil {
		s.Who = append(s.Who, p.AllowedUsers...)
		for _, g := range p.AllowedGroups {
			s.Who = append(s.Who, "group "+g)
		}
		if p.AllowAgents {
			for _, a := range p.AllowedAgents {
				s.Who = append(s.Who, "agent "+a)
			}
		}
	}
	var who Approver
	countedAs := ""
	if id != nil {
		who = Approver{User: id.User, Groups: id.Groups, Actor: id.Actor}
		counted, ok := t.countedKeys[actor.PartyKey(who.actorOf())]
		s.AlreadyCounted, countedAs = ok, counted
	}
	switch {
	case t.Held != "":
		s.Block, s.Reason = approvalPolicyBlock(p, len(gate.Spec.Approvals), author), t.Held
	case len(t.Rejecters) > 0:
		s.Block, s.Reason = ApprovalRejected, "rejected by "+joinUsers(t.Rejecters)
	case id == nil:
	case s.AlreadyCounted && countedAs == id.User:
		s.Block, s.Reason = ApprovalCounted, "you approved it; it needs someone else"
	case s.AlreadyCounted:
		s.Block, s.Reason = ApprovalCounted, fmt.Sprintf("%s already approved for your party (one vote per party); it needs someone else", countedAs)
	default:
		s.Block = approvalDecisionBlock(p, who, author)
		ok, why := ApprovalDecisionCounts(p, who, author)
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
