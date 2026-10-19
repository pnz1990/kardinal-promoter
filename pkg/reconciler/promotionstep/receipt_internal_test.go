// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

package promotionstep

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
)

// TestReceiptPR: the receipt names the step's PR and its SCM provider, so
// the receipt reconciler comments on the right one; a step without a PR
// (approval: auto) has none.
//
// Covers RECEIPT-PR-01.
func TestReceiptPR(t *testing.T) {
	id := &v1alpha1.ScmProviderIdentity{Kind: "ScmProvider", Name: "gitlab"}
	ps := &v1alpha1.PromotionStep{Spec: v1alpha1.PromotionStepSpec{ScmProvider: id}}
	assert.Nil(t, receiptPR(ps))
	ps.Status.Outputs = map[string]string{"prURL": "https://github.com/acme/deploy/pull/412", "prNumber": "412"}
	assert.Equal(t, &v1alpha1.ReceiptPullRequest{Repo: "acme/deploy", Number: 412,
		URL: "https://github.com/acme/deploy/pull/412", ScmProvider: id}, receiptPR(ps))
	ps.Status.Outputs = nil
	ps.Status.PRURL = "https://github.com/acme/deploy/pull/7"
	assert.Equal(t, 7, receiptPR(ps).Number)
}

// TestActiveOverride: an overridden gate records the override in effect
// for its environment when it was evaluated, with its verification.
//
// Covers RECEIPT-STATEMENT-02.
func TestActiveOverride(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC))
	g := &v1alpha1.PolicyGate{
		Spec: v1alpha1.PolicyGateSpec{Overrides: []v1alpha1.PolicyGateOverride{
			{Reason: "uat only", Stage: "uat", ExpiresAt: metav1.NewTime(at.Add(time.Hour))},
			{Reason: "expired", ExpiresAt: metav1.NewTime(at.Add(-time.Hour))},
			{Reason: "hotfix", CreatedBy: "bob", ExpiresAt: metav1.NewTime(at.Add(time.Hour))},
		}},
		Status: v1alpha1.PolicyGateStatus{LastEvaluatedAt: &at, Reason: "OVERRIDDEN by bob: hotfix"},
	}
	o := activeOverride(g, "prod")
	assert.Equal(t, "hotfix", o.Reason)
	assert.Equal(t, "bob", o.CreatedBy)
	assert.False(t, o.Verified, "not recorded as checked")
}

// TestReceiptNeverBuilds: a statement that cannot be built and a Bundle or
// Pipeline that is gone are final; a cache error is retried.
func TestReceiptNeverBuilds(t *testing.T) {
	gone := apierrors.NewNotFound(schema.GroupResource{Group: "kardinal.io", Resource: "bundles"}, "b")
	assert.True(t, receiptNeverBuilds(fmt.Errorf("%w: %w", errReceiptBuild, receipt.ErrTooLarge)))
	assert.True(t, receiptNeverBuilds(fmt.Errorf("get bundle b: %w", gone)))
	assert.False(t, receiptNeverBuilds(errors.New("list metric checks: cache not synced")))
	assert.LessOrEqual(t, len(receiptErrorText(errors.New(strings.Repeat("é", 2000)))), 1024)
}

// TestDecisionChangeRequest: a change request's snapshot keeps at most 32
// approvers (counting the rest) with names cut to 256 bytes, and its texts
// cut as a gate's; past MaxDecisionBytes the approvers go with the long
// texts, before any gate.
func TestDecisionChangeRequest(t *testing.T) {
	cr := &v1alpha1.ChangeRequest{}
	cr.Labels = map[string]string{"kardinal.io/change-request-template": "prod-change"}
	cr.Name = "prod-change-prod--b"
	cr.Status.Ticket = &v1alpha1.ChangeRequestTicket{Key: "CHG1"}
	cr.Status.VerifiedReason = strings.Repeat("r", 3000)
	for i := 0; i < 40; i++ {
		cr.Status.Approvers = append(cr.Status.Approvers, v1alpha1.ChangeRequestApprover{Name: strings.Repeat("n", 300)})
	}
	got := decisionChangeRequest(cr)
	assert.Equal(t, "prod-change", got.Name)
	assert.Equal(t, "prod-change-prod--b", got.Instance)
	assert.True(t, got.Verified, "open mode: a ticket with verified unset is the promotion's")
	assert.Len(t, got.Approvers, 32)
	assert.Equal(t, 8, got.ApproversOmitted)
	assert.Len(t, got.Approvers[0].Name, 256)
	assert.Len(t, got.VerifiedReason, maxDecisionText)

	no := false
	cr.Status.Verified = &no
	assert.False(t, decisionChangeRequest(cr).Verified, "a link the reconciler did not verify")
	cr.Status.Ticket = nil
	cr.Status.Verified = nil
	assert.False(t, decisionChangeRequest(cr).Verified, "no ticket yet")

	d := &v1alpha1.PromotionDecision{Gates: []v1alpha1.DecisionGate{{Name: "g1", Ready: true}, {Name: "g2", Ready: true}}}
	for i := 0; i < v1alpha1.MaxDecisionChangeRequests; i++ {
		c := v1alpha1.DecisionChangeRequest{Name: "c", Instance: "i"}
		for j := 0; j < 32; j++ {
			c.Approvers = append(c.Approvers, v1alpha1.ChangeRequestApprover{Name: strings.Repeat("a", 256)})
		}
		d.ChangeRequests = append(d.ChangeRequests, c)
	}
	assert.True(t, fitDecision(d))
	assert.True(t, d.Truncated)
	assert.Len(t, d.Gates, 2, "no gate left out")
	assert.Empty(t, d.ChangeRequests[0].Approvers)
	assert.Equal(t, 32, d.ChangeRequests[0].ApproversOmitted)
}

// TestDecision_ChangeRequestCap: a snapshot records at most
// MaxDecisionChangeRequests change requests and says it is truncated.
func TestDecision_ChangeRequestCap(t *testing.T) {
	r := &Reconciler{}
	crs := make([]v1alpha1.ChangeRequest, v1alpha1.MaxDecisionChangeRequests+1)
	for i := range crs {
		crs[i].Name = fmt.Sprintf("c%02d", i)
	}
	ps := &v1alpha1.PromotionStep{}
	d := r.decision(t.Context(), ps, nil, crs, nil, nil)
	assert.Len(t, d.ChangeRequests, v1alpha1.MaxDecisionChangeRequests)
	assert.True(t, d.Truncated)
	d = r.decision(t.Context(), ps, nil, crs[:v1alpha1.MaxDecisionChangeRequests], nil, nil)
	assert.Len(t, d.ChangeRequests, v1alpha1.MaxDecisionChangeRequests)
	assert.False(t, d.Truncated)
}
