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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
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
