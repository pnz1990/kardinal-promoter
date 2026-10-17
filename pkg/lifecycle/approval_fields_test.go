// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// changeGatePolicy declares the fields of spec 23 section 8.1.
func changeGatePolicy() *v1alpha1.GateApprovalPolicy {
	return &v1alpha1.GateApprovalPolicy{Required: 1, ExcludeAuthor: true, Fields: []v1alpha1.ApprovalField{
		{Name: "ticketID", Type: "string", Required: true, Pattern: `^CHG[0-9]{7}$`, Description: "ServiceNow change number"},
		{Name: "riskAcknowledged", Type: "bool", Required: true, MustBe: "true"},
		{Name: "rollbackPlan", Type: "url"},
		{Name: "attempts", Type: "int"},
		{Name: "tier", Type: "enum", Values: []string{"low", "high"}},
		{Name: "note"}, // type unset: a string
	}}
}

// TestApprovalFieldProblems (spec 23 §8, CR6): each type, pattern, mustBe and
// size rule; a missing required field; fields the gate does not declare are
// ignored; a gate without fields accepts anything.
func TestApprovalFieldProblems(t *testing.T) {
	good := map[string]string{"ticketID": "CHG0031337", "riskAcknowledged": "true"}
	with := func(k, v string) map[string]string {
		m := map[string]string{"ticketID": "CHG0031337", "riskAcknowledged": "true"}
		m[k] = v
		return m
	}
	cases := []struct {
		name   string
		fields map[string]string
		want   string // "" when valid
	}{
		{"the required fields", good, ""},
		{"every field", map[string]string{"ticketID": "CHG0031337", "riskAcknowledged": "true", "rollbackPlan": "https://wiki/rb",
			"attempts": "-3", "tier": "high", "note": "anything"}, ""},
		{"none", nil, "riskAcknowledged is required; ticketID is required (ServiceNow change number)"},
		{"an empty required value", with("ticketID", ""), "ticketID is required (ServiceNow change number)"},
		{"pattern", with("ticketID", "CHG12"), `ticketID must match ^CHG[0-9]{7}$, not "CHG12"`},
		{"mustBe", with("riskAcknowledged", "false"), "riskAcknowledged must be true"},
		{"bool", with("riskAcknowledged", "yes"), `riskAcknowledged must be true or false, not "yes"`},
		{"url scheme", with("rollbackPlan", "ftp://x/y"), "rollbackPlan must be an http or https URL with a host"},
		{"url host", with("rollbackPlan", "https:///nohost"), "rollbackPlan must be an http or https URL with a host"},
		{"int", with("attempts", "1.5"), `attempts must be an integer, not "1.5"`},
		{"enum", with("tier", "medium"), `tier must be one of low, high, not "medium"`},
		{"too long", with("note", strings.Repeat("x", MaxApprovalFieldValue+1)), "note is longer than 1024 bytes"},
		{"invalid UTF-8", with("note", "\xff"), "note is not valid UTF-8"},
		{"undeclared fields are ignored", with("unknown", "whatever"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, why := ApprovalFieldsValid(changeGatePolicy(), tc.fields)
			if tc.want == "" {
				assert.True(t, ok, why)
				assert.Empty(t, why)
				return
			}
			assert.False(t, ok)
			assert.Contains(t, why, "approval.fields: ")
			assert.Contains(t, why, tc.want)
		})
	}

	t.Run("a pattern that does not compile cannot be satisfied", func(t *testing.T) {
		p := &v1alpha1.GateApprovalPolicy{Fields: []v1alpha1.ApprovalField{{Name: "x", Pattern: "("}}}
		ok, why := ApprovalFieldsValid(p, map[string]string{"x": "y"})
		assert.False(t, ok)
		assert.Contains(t, why, "x cannot be satisfied: its pattern does not compile")
	})
	t.Run("a gate without fields accepts any", func(t *testing.T) {
		for _, p := range []*v1alpha1.GateApprovalPolicy{nil, {Required: 1}} {
			ok, why := ApprovalFieldsValid(p, map[string]string{"x": "\xff"})
			assert.True(t, ok, why)
		}
	})
	t.Run("missing is marked", func(t *testing.T) {
		problems := ApprovalFieldProblems(changeGatePolicy(), map[string]string{"riskAcknowledged": "no"})
		require.Len(t, problems, 2)
		assert.Equal(t, FieldProblem{Name: "riskAcknowledged", Problem: `must be true or false, not "no"`}, problems[0])
		assert.True(t, problems[1].Missing)
		assert.Equal(t, "ticketID", problems[1].Name)
	})
}

// TestDeclaredFields: only the declared fields are kept, cut at a character
// boundary.
func TestDeclaredFields(t *testing.T) {
	p := changeGatePolicy()
	got := DeclaredFields(p, map[string]string{"ticketID": "CHG0031337", "note": strings.Repeat("é", 200), "unknown": "x"}, 10)
	assert.Equal(t, map[string]string{"ticketID": "CHG0031337", "note": "ééééé…"}, got)
	assert.Nil(t, DeclaredFields(p, map[string]string{"unknown": "x"}, 10))
	assert.Nil(t, DeclaredFields(&v1alpha1.GateApprovalPolicy{}, map[string]string{"x": "y"}, 10))
	assert.Equal(t, strings.Repeat("x", 2000), DeclaredFields(p, map[string]string{"note": strings.Repeat("x", 2000)}, 0)["note"], "0 keeps it whole")
}

// TestTallyApprovals_Fields (spec 23 §8.2, §8.3): an approve whose fields do
// not satisfy the declaration does not count and says why; a reject needs
// none; approvals.fields holds the values of the counted approvals only (not
// of an approver the gate does not allow, nor of a party's second vote, nor
// of a party whose reject wins); status records keep the declared fields only.
func TestTallyApprovals_Fields(t *testing.T) {
	p := changeGatePolicy()
	p.Required = 2
	p.AllowedUsers = []string{"bob", "carol", "dave", "erin"}
	ok := map[string]string{"ticketID": "CHG0000001", "riskAcknowledged": "true", "unknown": "x"}
	ok2 := map[string]string{"ticketID": "CHG0000002", "riskAcknowledged": "true"}
	approve := func(user string, fields map[string]string) v1alpha1.GateApproval {
		a := by(human(user), "approve")
		a.Fields = fields
		return a
	}
	gate := partyGate(p,
		approve("bob", ok),
		approve("carol", map[string]string{"ticketID": "CHG0000003"}), // riskAcknowledged missing
		approve("dave", ok2),
		approve("mallory", map[string]string{"ticketID": "CHG0000009", "riskAcknowledged": "true"}), // not allowed
		by(human("erin"), "reject"), // a reject needs no fields
	)
	tally := TallyApprovals(gate, *human("alice"))
	byUser := map[string]v1alpha1.GateApprovalStatus{}
	for _, r := range tally.Records {
		byUser[r.User] = r
	}
	assert.True(t, byUser["bob"].Counted)
	assert.False(t, byUser["carol"].Counted)
	assert.Equal(t, "approval.fields: riskAcknowledged is required", byUser["carol"].Reason)
	assert.True(t, byUser["dave"].Counted)
	assert.False(t, byUser["mallory"].Counted)
	assert.True(t, byUser["erin"].Counted, "a reject without fields counts")
	assert.Equal(t, map[string]string{"ticketID": "CHG0000001", "riskAcknowledged": "true"}, byUser["bob"].Fields,
		"only declared fields are recorded")
	assert.Equal(t, []string{"bob", "dave"}, tally.Approvers)
	assert.Equal(t, []string{"CHG0000001", "CHG0000002"}, tally.Fields["ticketID"],
		"the counted approvals' values, not carol's (not counted) nor mallory's (not allowed)")
	assert.Equal(t, []string{}, tally.Fields["rollbackPlan"], "a declared field nobody gave is an empty list")
	_, undeclared := tally.Fields["unknown"]
	assert.False(t, undeclared)
	assert.False(t, tally.Met(), "erin's reject blocks it")

	t.Run("a party whose reject wins gives no fields", func(t *testing.T) {
		p := changeGatePolicy()
		g := partyGate(p, approve("bob", ok), by(human("bob"), "reject"), approve("dave", ok2))
		tl := TallyApprovals(g, *human("alice"))
		assert.Equal(t, []string{"dave"}, tl.Approvers)
		assert.Equal(t, []string{"CHG0000002"}, tl.Fields["ticketID"])
	})
	t.Run("a party's second vote gives no fields", func(t *testing.T) {
		p := changeGatePolicy()
		p.AllowAgents, p.AllowedAgents = true, []string{"helper"}
		helper := by(agentActor("helper", "bob"), "approve")
		helper.Fields = ok2
		g := partyGate(p, approve("bob", ok), helper)
		tl := TallyApprovals(g, *human("alice"))
		assert.Equal(t, []string{"bob"}, tl.Approvers)
		assert.Equal(t, []string{"CHG0000001"}, tl.Fields["ticketID"], "bob and his agent are one party: one vote, its fields")
	})
	t.Run("no declared fields, no approvals.fields", func(t *testing.T) {
		g := partyGate(&v1alpha1.GateApprovalPolicy{Required: 1}, approve("bob", ok))
		tl := TallyApprovals(g, *human("alice"))
		assert.Nil(t, tl.Fields)
		assert.Nil(t, tl.Records[0].Fields)
		assert.True(t, tl.Met())
	})
}
