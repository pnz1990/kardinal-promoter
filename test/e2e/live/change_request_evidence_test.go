//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestGate_ChangeRequestEvidence (spec 23 section 9): a prod PR opened once
// its ServiceNow change is approved carries a Change Requests row with the
// change number linked to its page, approved and verified, and its
// approver; an auditor's evidence pack holds the instance in
// changerequests.ndjson and the template in changerequest-templates.ndjson.
// The receipt is TestReceipts_ChangeRequest's (the receipts suite).
//
// Covers CHANGE-REQUEST-12.
func TestGate_ChangeRequestEvidence(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test", "prod")
	snowTemplate(t, e, a.ns)
	e.CreateGate(t, framework.Gate(a.ns, "cab", "prod",
		`changeRequests["prod-change"].state == "approved" && !changeRequests["prod-change"].stale`, recheck))
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)

	name := "prod-change-prod--" + bundle
	inst := waitChangeRequest(t, e, a.ns, name, "the instance opened its ticket", func(cr *v1alpha1.ChangeRequest) (bool, string) {
		return cr.Status.Ticket != nil && cr.Status.State == "pending", fmt.Sprintf("state %q conditions %v", cr.Status.State, cr.Status.Conditions)
	})
	tk := inst.Status.Ticket
	require.NotEmpty(t, tk.URL)
	e.SetFakeSnowTicket(t, tk.Key, map[string]interface{}{"state": "-2", "approval": "approved",
		"approvers": []map[string]string{{"name": "Jane Smith", "state": "approved"}}})
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)

	pr := a.openPR(t, bundle, "prod")
	inst = getChangeRequest(t, e, a.ns, name)
	assert.Contains(t, pr.Body, "### Change Requests\n\n| Change request | Ticket | Provider | State | Verified | Approvers | Read at |\n|---|---|---|---|---|---|---|\n"+
		"| prod-change | ["+tk.Key+"]("+tk.URL+") | servicenow | approved ("+inst.Status.ProviderState+") | yes | Jane Smith | ",
		"the PR names the approved change")
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)

	pack := filepath.Join(t.TempDir(), "pack.tar.gz")
	res := c.Exec(framework.CLIOptions{Kubeconfig: auditorKubeconfig(t, e, "", "auditor@example.com")},
		c.Args(a.ns, "audit", "evidence-pack", "-n", a.ns, "--since", "1h", "-o", pack)...)
	require.Zero(t, res.Code, res.Output())
	dir, m := packOf(t, pack)
	assert.Empty(t, m["coverage"].(map[string]interface{})["forbidden"], "the auditor role reads change requests")
	crs := ndjson(t, dir, "changerequests.ndjson")
	assert.Equal(t, []string{a.ns + "/" + name}, names(crs))
	require.Len(t, crs, 1)
	ticket := crs[0]["status"].(map[string]interface{})["ticket"].(map[string]interface{})
	assert.Equal(t, tk.Key, ticket["key"])
	assert.Equal(t, []string{a.ns + "/prod-change"}, names(ndjson(t, dir, "changerequest-templates.ndjson")))
}

func getChangeRequest(t *testing.T, e *framework.Env, ns, name string) *v1alpha1.ChangeRequest {
	t.Helper()
	var cr v1alpha1.ChangeRequest
	require.NoError(t, e.Client.Get(t.Context(), types.NamespacedName{Namespace: ns, Name: name}, &cr))
	return &cr
}

// TestReceipts_ChangeRequest (spec 23 sections 8.3 and 9): a prod gate
// needs its ServiceNow change approved and an approval naming that change
// in its ticketID field. The prod receipt records the change as the step
// started on it (template, instance, key, URL, approved, verified, its
// approver, when it was read) and the approval with its fields; the test
// receipt has no change request.
//
// Covers CHANGE-REQUEST-13.
func TestReceipts_ChangeRequest(t *testing.T) {
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test", "prod")
	snowTemplate(t, e, a.ns)
	g := framework.Gate(a.ns, "cab", "prod", `changeRequests["prod-change"].state == "approved" && `+
		`approvals.fields.ticketID.exists(id, id == changeRequests["prod-change"].ticket)`, recheck)
	g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, Fields: []v1alpha1.ApprovalField{
		{Name: "ticketID", Required: true, Description: "ServiceNow change number"}}}
	e.CreateGate(t, g)
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)

	name := "prod-change-prod--" + bundle
	inst := waitChangeRequest(t, e, a.ns, name, "the instance opened its ticket", func(cr *v1alpha1.ChangeRequest) (bool, string) {
		return cr.Status.Ticket != nil && cr.Status.State == "pending", fmt.Sprintf("state %q conditions %v", cr.Status.State, cr.Status.Conditions)
	})
	tk := inst.Status.Ticket
	e.SetFakeSnowTicket(t, tk.Key, map[string]interface{}{"state": "-2", "approval": "approved",
		"approvers": []map[string]string{{"name": "Jane Smith", "state": "approved"}}})
	alice := userKubeconfig(t, e, a.ns, "alice@example.com", "release-managers")
	r := c.Exec(framework.CLIOptions{Kubeconfig: alice}, c.Args(a.ns, "approve", pipelineName, "prod", "--field", "ticketID="+tk.Key)...)
	require.Zero(t, r.Code, r.Output())
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)

	rc := promotionReceipt(t, e, a.ns, bundle, "prod", v1alpha1.ReceiptStored)
	st, err := receipt.Parse([]byte(rc.Spec.Statement))
	require.NoError(t, err)
	rec := st.Predicate.Kardinal
	assert.True(t, rec.DecisionSnapshot)
	require.Len(t, rec.ChangeRequests, 1, "the prod ticket, as the step started on it")
	got := rec.ChangeRequests[0]
	assert.Equal(t, "prod-change", got.Name)
	assert.Equal(t, name, got.Instance)
	assert.Equal(t, "servicenow", got.Provider)
	assert.Equal(t, tk.Key, got.Ticket)
	assert.Equal(t, tk.URL, got.URL)
	assert.Equal(t, "approved", got.State)
	assert.True(t, got.Verified, "kardinal opened it with the correlation id")
	assert.Equal(t, []string{"Jane Smith"}, got.Approvers)
	assert.NotEmpty(t, got.ObservedAt)
	assert.NotEmpty(t, got.ValidUntil)
	var fields map[string]string
	for _, gate := range rec.Gates {
		for _, ap := range gate.Approvals {
			if ap.User == "alice@example.com" && ap.Counted {
				fields = ap.Fields
			}
		}
	}
	assert.Equal(t, map[string]string{"ticketID": tk.Key}, fields, "the counted approval and its fields")
	testRC := promotionReceipt(t, e, a.ns, bundle, "test", v1alpha1.ReceiptStored)
	testSt, err := receipt.Parse([]byte(testRC.Spec.Statement))
	require.NoError(t, err)
	assert.Empty(t, testSt.Predicate.Kardinal.ChangeRequests, "test has no change request")
}
