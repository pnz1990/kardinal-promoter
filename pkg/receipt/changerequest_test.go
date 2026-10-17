// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestBuild_ChangeRequestsAndFields (spec 23 section 9): the receipt records
// the step's change request tickets as its snapshot holds them (template,
// instance, provider, key and URL, state, whether the ticket is the
// promotion's and why, approvers, when it was read and until when that
// reading counted) and each approval's structured fields. No level is
// claimed for them.
//
// Covers CHANGE-REQUEST-11.
func TestBuild_ChangeRequestsAndFields(t *testing.T) {
	observed := metav1.NewTime(verifiedAt.Add(-61 * time.Minute))
	valid := metav1.NewTime(verifiedAt.Add(-51 * time.Minute))
	in := input("image")
	in.Decision.Gates[0].Approvals[0].Fields = map[string]string{"ticketID": "CHG0031337", "riskAcknowledged": "true"}
	in.Decision.ChangeRequests = []v1alpha1.DecisionChangeRequest{
		{Name: "prod-change", Instance: "prod-change-prod--checkout-1-5-0", Provider: "servicenow",
			Ticket: &v1alpha1.ChangeRequestTicket{Key: "CHG0031337", ID: "sys1", URL: "https://acme.service-now.com/c/sys1"},
			State:  "approved", ProviderState: "Scheduled", Verified: true, VerifiedReason: "the ticket carries the correlation id",
			Approvers:  []v1alpha1.ChangeRequestApprover{{Name: "Jane Smith", Decision: "approved"}, {Name: "Raj Patel"}},
			ObservedAt: &observed, ValidUntil: &valid},
		{Name: "jira-change", Instance: "jira-change-prod--checkout-1-5-0", Provider: "jira", TimedOut: true},
	}
	st, _ := build(t, in)
	r := st.Predicate.Kardinal
	assert.Equal(t, []ChangeRequest{
		{Name: "prod-change", Instance: "prod-change-prod--checkout-1-5-0", Provider: "servicenow", Ticket: "CHG0031337",
			URL: "https://acme.service-now.com/c/sys1", State: "approved", ProviderState: "Scheduled", Verified: true,
			VerifiedReason: "the ticket carries the correlation id", Approvers: []string{"Jane Smith", "Raj Patel"},
			ObservedAt: "2026-10-14T09:23:03Z", ValidUntil: "2026-10-14T09:33:03Z"},
		{Name: "jira-change", Instance: "jira-change-prod--checkout-1-5-0", Provider: "jira", TimedOut: true},
	}, r.ChangeRequests)
	assert.Equal(t, map[string]string{"ticketID": "CHG0031337", "riskAcknowledged": "true"}, r.Gates[0].Approvals[0].Fields)
	assert.Nil(t, r.Gates[0].Approvals[1].Fields)
	assert.Equal(t, []string{LevelGatesPassed, LevelApproved, LevelHealthy}, st.Predicate.VerifiedLevels)

	t.Run("none", func(t *testing.T) {
		st, b := build(t, input("image"))
		assert.Empty(t, st.Predicate.Kardinal.ChangeRequests)
		assert.NotContains(t, string(b), "changeRequests", "left out, not written empty")
	})
}

// TestBuild_ChangeRequestApproversCut: past the size cap the change
// requests' approvers go with the long texts (counted in approversOmitted),
// before any gate is left out; the tickets themselves stay.
func TestBuild_ChangeRequestApproversCut(t *testing.T) {
	in := input("image")
	for i := 0; i < 32; i++ {
		c := v1alpha1.DecisionChangeRequest{Name: "c", Instance: "i", Provider: "jira",
			Ticket: &v1alpha1.ChangeRequestTicket{Key: "OPS-1"}, State: "approved", Verified: true}
		for j := 0; j < 32; j++ {
			c.Approvers = append(c.Approvers, v1alpha1.ChangeRequestApprover{Name: strings.Repeat("a", 256)})
		}
		in.Decision.ChangeRequests = append(in.Decision.ChangeRequests, c)
	}
	b, _, err := Build(in)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(b), v1alpha1.MaxReceiptStatementBytes)
	st, err := Parse(b)
	require.NoError(t, err)
	r := st.Predicate.Kardinal
	assert.True(t, r.Truncated)
	require.Len(t, r.ChangeRequests, 32)
	for _, c := range r.ChangeRequests {
		assert.Empty(t, c.Approvers)
		assert.Equal(t, 32, c.ApproversOmitted)
		assert.Equal(t, "OPS-1", c.Ticket)
	}
	assert.Len(t, r.Gates, 1)
	assert.Zero(t, r.OmittedGates)
}

// TestBuild_ChangeRequestRedacted: a ticket URL or reason with a credential
// (userinfo, ?access_token=) is redacted before the receipt is signed, and
// the path is listed in redacted.
func TestBuild_ChangeRequestRedacted(t *testing.T) {
	in := input("image")
	in.Decision.ChangeRequests = []v1alpha1.DecisionChangeRequest{{Name: "c", Instance: "i", Provider: "jira",
		Ticket:         &v1alpha1.ChangeRequestTicket{Key: "OPS-1", URL: "https://svc:TICKETPW@jira.example.com/browse/OPS-1?access_token=TICKETTOKEN"},
		VerifiedReason: "linked from https://u:REASONPW@jira.example.com/x"}}
	st, b := build(t, in)
	for _, secret := range []string{"TICKETPW", "TICKETTOKEN", "REASONPW"} {
		assert.NotContains(t, string(b), secret)
	}
	assert.Contains(t, st.Predicate.Kardinal.Redacted, "kardinalPromotion.changeRequests[0].url")
	assert.Contains(t, st.Predicate.Kardinal.Redacted, "kardinalPromotion.changeRequests[0].verifiedReason")
}
