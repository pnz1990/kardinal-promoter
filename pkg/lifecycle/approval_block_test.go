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
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestApprovalStanding_Block: the standing says why as a typed Block, in
// the order the reconciler decides: held (the policy cannot be met) before
// rejected, then the caller's own; Reason says the same in words.
func TestApprovalStanding_Block(t *testing.T) {
	gate := func(p *kardinalv1alpha1.GateApprovalPolicy, decisions ...kardinalv1alpha1.GateApproval) *kardinalv1alpha1.PolicyGate {
		g := &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
			"kardinal.io/bundle": "b", "kardinal.io/environment": "prod"}}}
		g.Spec.Approval = p
		for _, d := range decisions {
			d.Bundle, d.Environment = "b", "prod"
			g.Spec.Approvals = append(g.Spec.Approvals, d)
		}
		return g
	}
	approve := func(u string) kardinalv1alpha1.GateApproval {
		return kardinalv1alpha1.GateApproval{User: u, Decision: "approve"}
	}
	reject := func(u string) kardinalv1alpha1.GateApproval {
		return kardinalv1alpha1.GateApproval{User: u, Decision: "reject"}
	}
	many := make([]kardinalv1alpha1.GateApproval, 0, MaxGateApprovals+1)
	for i := 0; i <= MaxGateApprovals; i++ {
		many = append(many, approve(fmt.Sprintf("u%d", i)))
	}
	me := &ApproverIdentity{User: "alice"}
	cases := []struct {
		name    string
		gate    *kardinalv1alpha1.PolicyGate
		creator string
		id      *ApproverIdentity
		want    ApprovalBlock
		held    bool
	}{
		{"open", gate(&kardinalv1alpha1.GateApprovalPolicy{Required: 1}), "bob", me, ApprovalOpen, false},
		{"too many", gate(&kardinalv1alpha1.GateApprovalPolicy{Required: 1}, many...), "bob", me, ApprovalTooMany, true},
		{"too many and rejected: held wins", gate(&kardinalv1alpha1.GateApprovalPolicy{Required: 1}, append(many, reject("carol"))...), "bob", me, ApprovalTooMany, true},
		{"no creator", gate(&kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}), "", me, ApprovalNoCreator, true},
		{"no creator and rejected: held wins", gate(&kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}, reject("carol")), "", me, ApprovalNoCreator, true},
		{"component creator", gate(&kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}), "subscription:default/x", me, ApprovalComponentCreator, true},
		{"rejected", gate(&kardinalv1alpha1.GateApprovalPolicy{Required: 1}, reject("carol")), "bob", me, ApprovalRejected, false},
		{"rejected, caller unknown", gate(&kardinalv1alpha1.GateApprovalPolicy{Required: 1}, reject("carol")), "bob", nil, ApprovalRejected, false},
		{"counted", gate(&kardinalv1alpha1.GateApprovalPolicy{Required: 2}, approve("alice")), "bob", me, ApprovalCounted, false},
		{"not allowed", gate(&kardinalv1alpha1.GateApprovalPolicy{Required: 1, AllowedUsers: []string{"dave"}}), "bob", me, ApprovalNotAllowed, false},
		{"the author", gate(&kardinalv1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true}), "alice", me, ApprovalAuthor, false},
		{"caller unknown", gate(&kardinalv1alpha1.GateApprovalPolicy{Required: 1}), "bob", nil, ApprovalOpen, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := ApprovalStandingOf(tc.gate, tc.creator, tc.id)
			assert.Equal(t, tc.want, st.Block)
			assert.Equal(t, tc.held, st.Block.Held())
			assert.Equal(t, tc.want == ApprovalOpen, st.Reason == "", "Reason says Block in words: %q", st.Reason)
			assert.Equal(t, tc.want == ApprovalOpen && tc.id != nil, st.CanApprove)
		})
	}
}
