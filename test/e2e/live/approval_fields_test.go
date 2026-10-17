//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestGate_ApprovalFields (spec 23 §8): a prod gate that asks every approve
// for a change ticket and an acknowledgement, and whose expression reads the
// ticket the counted approvals gave. kardinal why names the fields and the
// approve command with them; kardinal approve without them, or with a value
// the gate does not accept, is refused before anything is written; an
// Approval written with kubectl without them is admitted and recorded, but
// does not count, and the gate says why; the API server refuses a field name
// or value its CRD rules do not admit; with the fields, the approval counts,
// prod promotes, and the ApprovalRecorded AuditEvent and status.approvals
// carry them.
//
// Covers APPROVAL-FIELDS-02, CLI-APPROVE-FIELDS-01.
func TestGate_ApprovalFields(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	g := framework.Gate(a.ns, "change", "prod", `approvals.fields.ticketID.exists(id, id.startsWith("CHG"))`, recheck)
	g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, Fields: []v1alpha1.ApprovalField{
		{Name: "ticketID", Required: true, Pattern: `^CHG[0-9]{7}$`, Description: "ServiceNow change number"},
		{Name: "riskAcknowledged", Type: "bool", Required: true, MustBe: "true", Description: "I have read the migration notes"},
	}}
	e.CreateGate(t, g)
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	// The expression reads the counted approvals' tickets: false until one
	// counts, and the reason says so.
	e.WaitGateReady(t, a.ns, bundle, "prod", "change", false, `approvals.fields.ticketID.exists(id, id.startsWith("CHG")) = false`, gateTimeout)

	why := e.MustKardinal(t, a.ns, "why", pipelineName, "prod")
	assert.Contains(t, why, "ticketID (ServiceNow change number), riskAcknowledged true", "why names the fields")
	assert.Contains(t, why, "--field ticketID=<ticketID> --field riskAcknowledged=true", "and the approve command with them")

	alice := userKubeconfig(t, e, a.ns, "alice@example.com", "release-managers")
	as := func(args ...string) framework.CLIResult {
		return c.Exec(framework.CLIOptions{Kubeconfig: alice}, c.Args(a.ns, args...)...)
	}
	approvalsOf := func() []v1alpha1.Approval {
		var l v1alpha1.ApprovalList
		require.NoError(t, e.Client.List(ctx, &l, client.InNamespace(a.ns)))
		return l.Items
	}
	r := as("approve", pipelineName, "prod", "--field", "ticketID=CHG0031337")
	require.NotEqual(t, 0, r.Code, r.Output())
	assert.Contains(t, r.Output(), "gate change asks for field riskAcknowledged (I have read the migration notes): give it with --field riskAcknowledged=true; nothing recorded")
	r = as("approve", pipelineName, "prod", "--field", "ticketID=CHG12", "--field", "riskAcknowledged=true")
	require.NotEqual(t, 0, r.Code, r.Output())
	assert.Contains(t, r.Output(), "gate change: approval.fields: ticketID must match ^CHG[0-9]{7}$")
	assert.Empty(t, approvalsOf(), "nothing written")

	// Written by hand, without the fields: admitted (the CRD checks the shape
	// only), recorded, not counted.
	forBob := impersonating(t, e, a.ns, "bob@example.com", nil, []string{"approvals"}, "create", "delete")
	b := getBundle(t, e, a.ns, bundle)
	byHand := func(name string, fields map[string]string) *v1alpha1.Approval {
		return &v1alpha1.Approval{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.ns,
			Labels: map[string]string{"kardinal.io/bundle": bundle, "kardinal.io/environment": "prod"}},
			Spec: v1alpha1.ApprovalSpec{Bundle: bundle, BundleUID: string(b.UID), Environment: "prod", User: "bob@example.com",
				Groups: []string{}, Decision: "approve", Fields: fields}}
	}
	err := forBob.Create(ctx, byHand("bob-bad-name", map[string]string{"ticket-id": "CHG0031337"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a field name is a letter then letters, digits or underscores")
	err = forBob.Create(ctx, byHand("bob-long", map[string]string{"ticketID": strings.Repeat("x", 1025)}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a field value is at most 1024 bytes")
	require.NoError(t, forBob.Create(ctx, byHand("bob-without", nil)))
	framework.Eventually(t, gateTimeout, "bob's Approval recorded and not counted", func(ctx context.Context) (bool, string) {
		gate, ok, err := e.GateInstance(ctx, a.ns, bundle, "prod", "change")
		if err != nil || !ok {
			return false, fmt.Sprintf("no gate (%v)", err)
		}
		for _, rec := range gate.Status.Approvals {
			if rec.User == "bob@example.com" {
				return !rec.Counted && rec.Reason == "approval.fields: riskAcknowledged is required (I have read the migration notes); ticketID is required (ServiceNow change number)",
					fmt.Sprintf("counted=%v reason=%q", rec.Counted, rec.Reason)
			}
		}
		return false, fmt.Sprintf("%d records", len(gate.Status.Approvals))
	})
	a.noStep(t, bundle, "prod", 10*time.Second)

	// With the fields: counted, the expression reads the ticket, prod promotes.
	r = as("approve", pipelineName, "prod", "--field", "ticketID=CHG0031337", "--field", "riskAcknowledged=true")
	require.Equal(t, 0, r.Code, r.Output())
	e.WaitGateReady(t, a.ns, bundle, "prod", "change", true, "", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	gate, ok, err := e.GateInstance(ctx, a.ns, bundle, "prod", "change")
	require.NoError(t, err)
	require.True(t, ok)
	var mine *v1alpha1.GateApprovalStatus
	for i := range gate.Status.Approvals {
		if gate.Status.Approvals[i].User == "alice@example.com" {
			mine = &gate.Status.Approvals[i]
		}
	}
	require.NotNil(t, mine)
	assert.True(t, mine.Counted)
	assert.Equal(t, map[string]string{"ticketID": "CHG0031337", "riskAcknowledged": "true"}, mine.Fields)
	framework.Eventually(t, time.Minute, "an ApprovalRecorded AuditEvent with the fields", func(ctx context.Context) (bool, string) {
		events, err := e.AuditEvents(ctx, a.ns, bundle)
		if err != nil {
			return false, err.Error()
		}
		for _, ev := range events {
			if ev.Spec.Action == "ApprovalRecorded" && ev.Spec.Fields["ticketID"] == "CHG0031337" {
				return strings.Contains(ev.Spec.Message, "fields: riskAcknowledged=true, ticketID=CHG0031337"), ev.Spec.Message
			}
		}
		return false, fmt.Sprintf("%d events", len(events))
	})
}
