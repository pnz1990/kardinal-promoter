// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
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
		// An approval names who decided: without TokenReview it is refused, with why.
		{"no TokenReview: nobody named, all allowed but approving", "", uiAuthConfig{}, "", []bool{false, true, true, true}},
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
			if tt.wantUser == "" {
				assert.Contains(t, resp.Results[0].Reason, "approving needs sign-in with your Kubernetes identity")
			} else {
				assert.Empty(t, resp.Results[0].Reason)
			}
			assert.Equal(t, []string{"approver or admin", "approver or admin", "promoter or admin", "promoter or admin"},
				[]string{resp.Results[0].GrantedBy, resp.Results[1].GrantedBy, resp.Results[2].GrantedBy, resp.Results[3].GrantedBy})
		})
	}
}

// TestUIHandler_Access_Errors: POST /access answers only for the UI's own
// actions, with valid names, at most 32 checks and a small body; anything else
// is a 400 before any review. A failing review is a 503, not an allowed action
// (QA #1748).
//
// Covers UI-ACCESS-02.
func TestUIHandler_Access_Errors(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{"a": "approver"}}
	check := func(c string) string { return `{"namespace":"team-a","checks":[` + c + `]}` }
	for _, tt := range []struct {
		name, body string
		scope      string
		access     uiAccessReviewerFunc
		want       int
	}{
		{"no checks", `{"checks":[]}`, "", nil, http.StatusBadRequest},
		{"bad verb", check(`{"verb":"escalate","resource":"bundles"}`), "", nil, http.StatusBadRequest},
		{"a kind the UI does not ask about", check(`{"verb":"get","resource":"secrets"}`), "", nil, http.StatusBadRequest},
		{"a path in the resource", check(`{"verb":"update","resource":"secrets/../x","subresource":"anything"}`), "", nil, http.StatusBadRequest},
		{"an unknown subresource", check(`{"verb":"update","resource":"pipelines","subresource":"anything"}`), "", nil, http.StatusBadRequest},
		{"a namespace that is not a DNS-1123 label", `{"namespace":"Not_A Namespace/x","checks":[{"verb":"create","resource":"bundles"}]}`, "", nil, http.StatusBadRequest},
		{"a 20 KB namespace", `{"namespace":"` + strings.Repeat("x", 20<<10) + `","checks":[{"verb":"create","resource":"bundles"}]}`, "", nil, http.StatusBadRequest},
		{"a name that is not a DNS-1123 subdomain", check(`{"verb":"update","resource":"pipelines","subresource":"pause","name":"A/B"}`), "", nil, http.StatusBadRequest},
		{"too many", `{"checks":[` + repeatChecks(33) + `]}`, "", nil, http.StatusBadRequest},
		{"another namespace than --watch-namespace", check(`{"verb":"create","resource":"bundles"}`), "team-b", nil, http.StatusBadRequest},
		{"review down", check(`{"verb":"create","resource":"bundles"}`), "", failingAccess, http.StatusServiceUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			acc := uiAccessReviewerFunc(func(ctx context.Context, u authv1.UserInfo, a authzv1.ResourceAttributes) (bool, string, error) {
				calls++
				return exactAccess{}.Allowed(ctx, u, a)
			})
			if tt.access != nil {
				acc = tt.access
			}
			h := newUIHandler(fake.NewClientBuilder().WithScheme(uiScheme()).Build(), nil,
				uiAuthConfig{tokens: tokens, access: acc, scopeNamespace: tt.scope}, "", nil, zerolog.Nop())
			rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/access", "Bearer a", tt.body)
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
			if tt.want == http.StatusBadRequest {
				assert.Zero(t, calls, "a refused request reviews nothing")
			}
		})
	}
}

// TestUIHandler_Access_Bounded: the same check twice is reviewed once, and a
// caller sending 200 requests of 32 distinct names (6,400 checks) does not
// cause 6,400 SubjectAccessReviews: the per-user limit stops them, and the
// requests past it are 429s (QA #1748).
//
// Covers UI-ACCESS-02.
func TestUIHandler_Access_Bounded(t *testing.T) {
	tokens := &uiTestTokens{users: map[string]string{"v": "viewer"}}
	calls := 0
	inner := uiAccessReviewerFunc(func(context.Context, authv1.UserInfo, authzv1.ResourceAttributes) (bool, string, error) {
		calls++
		return false, "", nil
	})
	access := uiauth.NewCachedAccessReviewer(uiauth.NewRateLimitedAccessReviewer(inner,
		uiauth.DefaultAccessReviewsPerUserPerMinute, uiauth.DefaultAccessReviewsPerMinute), uiauth.DefaultCacheTTL)
	h := newUIHandler(fake.NewClientBuilder().WithScheme(uiScheme()).Build(), nil,
		uiAuthConfig{tokens: tokens, access: uiauth.NewCachedAccessReviewer(inner, uiauth.DefaultCacheTTL), accessCheck: access},
		"", nil, zerolog.Nop())

	rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/access", "Bearer v",
		`{"checks":[{"verb":"create","resource":"bundles"},{"verb":"create","resource":"bundles"}]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp uiAccessResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp.Results, 2)
	assert.Equal(t, 1, calls, "the same check twice is one review")

	limited := 0
	for r := 0; r < 200; r++ {
		checks := make([]string, 32)
		for i := range checks {
			checks[i] = fmt.Sprintf(`{"verb":"update","resource":"pipelines","subresource":"pause","name":"p-%d-%d"}`, r, i)
		}
		rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/access", "Bearer v", `{"checks":[`+strings.Join(checks, ",")+`]}`)
		if rec.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	assert.LessOrEqual(t, calls, uiauth.DefaultAccessReviewsPerUserPerMinute, "6,400 distinct checks do not reach the API server")
	assert.Positive(t, limited, "past the limit the answer is 429")
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
		out += fmt.Sprintf(`{"verb":"update","resource":"pipelines","subresource":"pause","name":"p%d"}`, i)
	}
	return out
}

var errReviewDown = errors.New("review API down")
