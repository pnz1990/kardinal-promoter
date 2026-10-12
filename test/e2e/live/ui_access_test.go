//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestUI_APIAccess checks POST /api/v1/ui/access live, in TokenReview mode
// with the chart's roles: each caller is named as TokenReview verified them,
// and each check is answered for them by a SubjectAccessReview (a "no" is
// allowed: false, not a 403), with the chart role that grants it. The viewer
// may take none of the UI's actions, the promoter may ship, pause and reject
// but not approve or override, the approver may approve, override and reject
// but not ship or pause. A refused action then names that role in its 403.
//
// Covers UI-ACCESS-01.
func TestUI_APIAccess(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns, _, ui, viewer, promoter, approver := uiRejectOverrideSetup(t, e)
	checks := []map[string]string{
		{"verb": "create", "resource": "approvals"},
		{"verb": "update", "resource": "policygates", "subresource": "override"},
		{"verb": "create", "resource": "bundles"},
		{"verb": "update", "resource": "pipelines", "subresource": "pause", "name": pipelineName},
		{"verb": "update", "resource": "bundles", "subresource": "reject"},
	}
	grantedBy := []string{"approver or admin", "approver or admin", "promoter or admin", "promoter or admin", "promoter, approver or admin"}
	type answer struct {
		User *struct {
			Username string `json:"username"`
		} `json:"user"`
		Results []struct {
			Allowed   bool   `json:"allowed"`
			GrantedBy string `json:"grantedBy"`
		} `json:"results"`
	}
	for _, tt := range []struct {
		u    roleUser
		want []bool
	}{
		{viewer, []bool{false, false, false, false, false}},
		{promoter, []bool{false, false, true, true, true}},
		{approver, []bool{true, true, false, false, true}},
	} {
		r := ui(tt.u).Post(t, uiAPI+"/access", map[string]interface{}{"namespace": ns, "checks": checks})
		require.Equal(t, http.StatusOK, r.Status, "%s: %s", tt.u.name, r)
		var a answer
		r.JSON(t, &a)
		require.NotNil(t, a.User, "%s is named", tt.u.name)
		assert.Equal(t, tt.u.username(), a.User.Username)
		require.Len(t, a.Results, len(checks))
		for i, want := range tt.want {
			assert.Equal(t, want, a.Results[i].Allowed, "%s: %v", tt.u.name, checks[i])
			assert.Equal(t, grantedBy[i], a.Results[i].GrantedBy, "%v", checks[i])
		}
	}

	// The refusal the UI shows when an action is tried anyway names the role.
	r := ui(promoter).Post(t, uiAPI+"/override", map[string]interface{}{"pipeline": pipelineName, "namespace": ns, "gate": "hold",
		"environment": "test", "reason": "e2e"})
	assert.Equal(t, http.StatusForbidden, r.Status, "%s", r)
	assert.Contains(t, r.Body, "the chart's approver or admin role grants it")
}
