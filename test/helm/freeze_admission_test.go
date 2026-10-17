// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
)

// TestFreezeAdmission: a freeze names who froze and who let a pipeline
// through it, and the API server admits only the requester's own name
// (docs/design/18-actor-policy.md section 9.3). The controller, which writes
// freezes for the UI's authenticated callers, is exempt.
func TestFreezeAdmission(t *testing.T) {
	t.Parallel()
	vap := identityPolicy(t, "freezes")
	rules := vap.Spec.MatchConstraints.ResourceRules
	require.Len(t, rules, 1)
	assert.Equal(t, []string{"changewindows"}, rules[0].Resources)
	assert.ElementsMatch(t, []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update},
		rules[0].Operations)

	controller := "system:serviceaccount:" + releaseNS + ":kardinal-promoter"
	exemption := func(by string) map[string]interface{} {
		e := map[string]interface{}{"namespace": "payments", "pipeline": "checkout", "reason": "hotfix",
			"expiresAt": "2026-10-14T12:00:00Z"}
		if by != "" {
			e["createdBy"] = by
		}
		return e
	}
	window := func(createdBy string, exemptions ...map[string]interface{}) map[string]interface{} {
		freeze := map[string]interface{}{"actors": "agents"}
		if createdBy != "" {
			freeze["createdBy"] = createdBy
		}
		if len(exemptions) > 0 {
			list := make([]interface{}, len(exemptions))
			for i, e := range exemptions {
				list[i] = e
			}
			freeze["exemptions"] = list
		}
		return map[string]interface{}{"metadata": map[string]interface{}{"name": "kardinal-freeze-agents"},
			"spec": map[string]interface{}{"type": "blackout", "start": "2026-10-14T09:00:00Z", "freeze": freeze}}
	}
	plain := map[string]interface{}{"metadata": map[string]interface{}{"name": "holiday"},
		"spec": map[string]interface{}{"type": "blackout", "start": "2026-12-20T00:00:00Z", "end": "2027-01-02T00:00:00Z"}}
	withReason := func(w map[string]interface{}, r string) map[string]interface{} {
		w["spec"].(map[string]interface{})["reason"] = r
		return w
	}

	tests := []struct {
		name     string
		old, cur map[string]interface{}
		user     string
		want     bool
	}{
		{name: "freeze as self", cur: window("alice"), user: "alice", want: true},
		{name: "freeze in someone else's name", cur: window("bob"), user: "alice", want: false},
		{name: "freeze without createdBy", cur: window(""), user: "alice", want: false},
		{name: "the controller freezes for a UI user", cur: window("alice"), user: controller, want: true},
		{name: "a window that is not a freeze", cur: plain, user: "alice", want: true},
		{name: "the controller writes only freezes", cur: plain, user: controller, want: false},
		{name: "someone else updates the reason", old: window("alice"), cur: withReason(window("alice"), "longer"), user: "bob", want: true},
		{name: "rewrite who froze", old: window("alice"), cur: window("bob"), user: "bob", want: false},
		{name: "the controller cannot rewrite who froze", old: window("alice"), cur: window("bob"), user: controller, want: false},
		{name: "turn a window into a freeze", old: plain, cur: window("alice"), user: "alice", want: false},
		{name: "turn a freeze into a plain window", old: window("alice"), cur: plain, user: "alice", want: false},
		{name: "exempt as self", old: window("alice"), cur: window("alice", exemption("bob")), user: "bob", want: true},
		{name: "exempt in someone else's name", old: window("alice"), cur: window("alice", exemption("carol")), user: "bob", want: false},
		{name: "exempt without createdBy", old: window("alice"), cur: window("alice", exemption("")), user: "bob", want: false},
		{name: "a second exemption keeps the first", old: window("alice", exemption("bob")),
			cur: window("alice", exemption("bob"), exemption("carol")), user: "carol", want: true},
		{name: "edit someone else's exemption", old: window("alice", exemption("bob")),
			cur: window("alice", map[string]interface{}{"namespace": "payments", "pipeline": "checkout", "reason": "longer",
				"expiresAt": "2027-01-01T00:00:00Z", "createdBy": "bob"}), user: "carol", want: false},
		{name: "remove an exemption", old: window("alice", exemption("bob")), cur: window("alice"), user: "carol", want: true},
		{name: "create with a forged exemption", cur: window("alice", exemption("bob")), user: "alice", want: false},
		{name: "the controller exempts for a UI user", old: window("alice"), cur: window("alice", exemption("bob")), user: controller, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, admits(t, vap, tc.cur, tc.old, tc.user))
		})
	}
}
