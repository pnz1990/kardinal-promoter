// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestUIHandler_Access: POST /access answers, for the verified caller, which
// actions they may take and which role grants each, without turning a "no"
// into a 403; without TokenReview every action is allowed and no user is
// named.
func TestUIHandler_Access(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{"a": "approver", "v": "viewer"}}
	access := exactAccess{
		"approver create approvals team-a": true, "approver update policygates/override team-a": true,
	}
	body := `{"namespace":"team-a","checks":[
		{"verb":"create","resource":"approvals"},
		{"verb":"update","resource":"policygates","subresource":"override","name":"g"},
		{"verb":"update","resource":"pipelines","subresource":"pause","name":"app"},
		{"verb":"create","resource":"bundles"}]}`
	tests := []struct {
		name, token string
		auth        uiAuthConfig
		wantUser    string
		want        []bool
	}{
		{"approver", "a", uiAuthConfig{tokens: tokens, access: access}, "approver", []bool{true, true, false, false}},
		{"viewer: every no is an answer", "v", uiAuthConfig{tokens: tokens, access: access}, "viewer", []bool{false, false, false, false}},
		{"no TokenReview: allowed, nobody named", "", uiAuthConfig{}, "", []bool{true, true, true, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newUIHandler(fake.NewClientBuilder().WithScheme(uiScheme()).Build(), nil, tt.auth, "", nil, zerolog.Nop())
			token := ""
			if tt.token != "" {
				token = "Bearer " + tt.token
			}
			rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/access", token, body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var resp uiAccessResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			if tt.wantUser == "" {
				assert.Nil(t, resp.User)
			} else {
				require.NotNil(t, resp.User)
				assert.Equal(t, tt.wantUser, resp.User.Username)
			}
			require.Len(t, resp.Results, 4)
			for i, want := range tt.want {
				assert.Equal(t, want, resp.Results[i].Allowed, "check %d", i)
			}
			assert.Equal(t, []string{"approver or admin", "approver or admin", "promoter or admin", "promoter or admin"},
				[]string{resp.Results[0].GrantedBy, resp.Results[1].GrantedBy, resp.Results[2].GrantedBy, resp.Results[3].GrantedBy})
		})
	}
}

// TestUIHandler_Access_Errors: a malformed request is a 400; a failing
// access review is a 503, not an allowed action.
func TestUIHandler_Access_Errors(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{"a": "approver"}}
	for _, tt := range []struct {
		name, body string
		access     uiAccessReviewerFunc
		want       int
	}{
		{"no checks", `{"checks":[]}`, nil, http.StatusBadRequest},
		{"bad verb", `{"checks":[{"verb":"escalate","resource":"bundles"}]}`, nil, http.StatusBadRequest},
		{"too many", `{"checks":[` + repeatChecks(33) + `]}`, nil, http.StatusBadRequest},
		{"review down", `{"checks":[{"verb":"create","resource":"bundles"}]}`, failingAccess, http.StatusServiceUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var acc = uiAccessReviewerFunc(exactAccess{}.Allowed)
			if tt.access != nil {
				acc = tt.access
			}
			h := newUIHandler(fake.NewClientBuilder().WithScheme(uiScheme()).Build(), nil,
				uiAuthConfig{tokens: tokens, access: acc}, "", nil, zerolog.Nop())
			rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/access", "Bearer a", tt.body)
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
		})
	}
}

// uiAccessReviewerFunc is an access reviewer from a function.
type uiAccessReviewerFunc func(ctx context.Context, u authv1.UserInfo, attrs authzv1.ResourceAttributes) (bool, string, error)

func (f uiAccessReviewerFunc) Allowed(ctx context.Context, u authv1.UserInfo, attrs authzv1.ResourceAttributes) (bool, string, error) {
	return f(ctx, u, attrs)
}

var failingAccess = uiAccessReviewerFunc(func(context.Context, authv1.UserInfo, authzv1.ResourceAttributes) (bool, string, error) {
	return false, "", errReviewDown
})

func repeatChecks(n int) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += `{"verb":"get","resource":"bundles"}`
	}
	return out
}

var errReviewDown = errors.New("review API down")
