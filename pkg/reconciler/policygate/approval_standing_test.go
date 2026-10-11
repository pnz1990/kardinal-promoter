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

package policygate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestApprovalStanding_MatchesTheTally: what lifecycle.ApprovalStandingOf
// tells a caller (the CLI's inbox and why, the UI) is what this reconciler
// does with their approval. For each case, the caller's approve is added to
// the gate and tallied the way Reconcile tallies it: CanApprove holds exactly
// when that approve is counted, raises the count and the gate is not held or
// rejected.
func TestApprovalStanding_MatchesTheTally(t *testing.T) {
	alice := lifecycle.ApproverIdentity{User: "alice", Groups: []string{"release-managers", "system:authenticated"}}
	decision := func(user, d string, groups ...string) kardinalv1alpha1.GateApproval {
		return kardinalv1alpha1.GateApproval{Bundle: "b1", Environment: "prod", User: user, Decision: d, Groups: groups}
	}
	cases := []struct {
		name      string
		policy    kardinalv1alpha1.GateApprovalPolicy
		approvals []kardinalv1alpha1.GateApproval
		creator   string
		want      bool
		reason    string
	}{
		{"anyone may", kardinalv1alpha1.GateApprovalPolicy{Required: 1}, nil, "bob", true, ""},
		{"allowed user", kardinalv1alpha1.GateApprovalPolicy{AllowedUsers: []string{"alice"}}, nil, "bob", true, ""},
		{"allowed group", kardinalv1alpha1.GateApprovalPolicy{AllowedGroups: []string{"release-managers"}}, nil, "bob", true, ""},
		{"not allowed", kardinalv1alpha1.GateApprovalPolicy{AllowedUsers: []string{"carol"}}, nil, "bob", false,
			"not an allowed approver (approval.allowedUsers, approval.allowedGroups)"},
		{"the author", kardinalv1alpha1.GateApprovalPolicy{ExcludeAuthor: true}, nil, "alice", false, "the version's author (approval.excludeAuthor)"},
		{"excludeAuthor, no creator", kardinalv1alpha1.GateApprovalPolicy{ExcludeAuthor: true}, nil, "", false,
			"excludeAuthor cannot be enforced: the Bundle has no verified creator (annotation kardinal.io/created-by)"},
		{"excludeAuthor, a component created it", kardinalv1alpha1.GateApprovalPolicy{ExcludeAuthor: true}, nil, "subscription:demo/images", false,
			`excludeAuthor cannot be enforced: the Bundle was created by the kardinal component "subscription:demo/images", not a person; create it as yourself (kardinal create bundle, or the Bundle API with your own token)`},
		{"already approved", kardinalv1alpha1.GateApprovalPolicy{Required: 2}, []kardinalv1alpha1.GateApproval{decision("alice", "approve")}, "bob", false,
			"you approved it; it needs someone else"},
		{"approved, then someone rejected", kardinalv1alpha1.GateApprovalPolicy{Required: 2},
			[]kardinalv1alpha1.GateApproval{decision("carol", "reject")}, "bob", false, "rejected by carol"},
		{"a reject that does not count", kardinalv1alpha1.GateApprovalPolicy{AllowedUsers: []string{"alice"}},
			[]kardinalv1alpha1.GateApproval{decision("mallory", "reject")}, "bob", true, ""},
		{"others approved", kardinalv1alpha1.GateApprovalPolicy{Required: 3},
			[]kardinalv1alpha1.GateApproval{decision("bob", "approve"), decision("carol", "approve")}, "dave", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := tc.policy
			gate := &kardinalv1alpha1.PolicyGate{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{labelBundle: "b1", labelEnvironment: "prod"}},
				Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: "true", Approval: &policy, Approvals: tc.approvals},
			}
			st := lifecycle.ApprovalStandingOf(gate, tc.creator, &alice)
			assert.Equal(t, tc.want, st.CanApprove)
			assert.Equal(t, tc.reason, st.Reason)

			// The server's view: alice approves.
			before := tallyApprovals(gate, tc.creator)
			after := gate.DeepCopy()
			after.Spec.Approvals = append(after.Spec.Approvals, decision(alice.User, "approve", alice.Groups...))
			tally := tallyApprovals(after, tc.creator)
			counts := len(tally.Approvers) > len(before.Approvers) && tally.Held == "" && len(tally.Rejecters) == 0
			assert.Equal(t, counts, st.CanApprove, "CanApprove is what the reconciler does with the approval")
			assert.Equal(t, len(before.Approvers), st.Approved)
		})
	}
}
