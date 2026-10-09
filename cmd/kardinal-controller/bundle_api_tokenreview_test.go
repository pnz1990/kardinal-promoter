// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// tokenUsers authenticates tokens from a map; others are not authenticated.
type tokenUsers map[string]string

func (m tokenUsers) Review(_ context.Context, token string) (*authv1.TokenReviewStatus, error) {
	if token == "review-down" {
		return nil, errors.New("api server unreachable")
	}
	u, ok := m[token]
	return &authv1.TokenReviewStatus{Authenticated: ok, User: authv1.UserInfo{Username: u}}, nil
}

// rbacRules allows (user, verb, resource, namespace) tuples.
type rbacRules map[string]bool

func (r rbacRules) Allowed(_ context.Context, u authv1.UserInfo, a authzv1.ResourceAttributes) (bool, string, error) {
	return r[u.Username+" "+a.Verb+" "+a.Resource+" "+a.Namespace], "", nil
}

// TestBundleAPI_TokenReview: with --bundle-api-tokenreview-auth a Kubernetes
// token is authenticated with TokenReview and the caller needs get on the
// Pipeline and create on bundles in the namespace; the Bundle records the
// caller in kardinal.io/requested-by. The static token still works and acts
// as the controller.
func TestBundleAPI_TokenReview(t *testing.T) {
	ci := "system:serviceaccount:team-a:ci"
	rules := rbacRules{
		ci + " get pipelines team-a": true, ci + " create bundles team-a": true,
		"viewer get pipelines team-a": true,
	}
	body := `{"pipeline":"app","namespace":"team-a","type":"image","images":[{"repository":"r","tag":"1"}]}`
	tests := []struct {
		name, token  string
		status       int
		wantBody     string
		wantBundle   bool
		wantRequestr string
	}{
		{name: "static token", token: "static", status: 201, wantBundle: true},
		{name: "authorized service account", token: "ci-token", status: 201, wantBundle: true, wantRequestr: ci},
		{name: "may read the Pipeline but not create bundles", token: "viewer-token", status: 403,
			wantBody: `forbidden: user "viewer" cannot create bundles.kardinal.io in namespace team-a`},
		{name: "unauthenticated token", token: "nope", status: 401, wantBody: "unauthorized"},
		{name: "review API down", token: "review-down", status: 503, wantBody: "auth unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := bundleAPIClient()
			srv := newBundleAPIServer(c, "static", "default")
			srv.enableTokenReview(tokenUsers{"ci-token": ci, "viewer-token": "viewer"}, rules)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+tt.token)
			w := httptest.NewRecorder()
			srv.Handler()(w, req)
			require.Equal(t, tt.status, w.Code, w.Body.String())
			if tt.wantBody != "" {
				assert.Contains(t, w.Body.String(), tt.wantBody)
			}
			var bundles v1alpha1.BundleList
			require.NoError(t, c.List(context.Background(), &bundles, client.InNamespace("team-a")))
			if !tt.wantBundle {
				assert.Empty(t, bundles.Items)
				return
			}
			require.Len(t, bundles.Items, 1)
			if tt.wantRequestr == "" {
				assert.NotContains(t, bundles.Items[0].Annotations, "kardinal.io/requested-by")
			} else {
				assert.Equal(t, tt.wantRequestr, bundles.Items[0].Annotations["kardinal.io/requested-by"])
			}
		})
	}
}

// TestBundleAPI_TokenReviewWithoutStaticToken: with no static token only
// Kubernetes tokens are accepted.
func TestBundleAPI_TokenReviewWithoutStaticToken(t *testing.T) {
	srv := newBundleAPIServer(bundleAPIClient(), "", "default")
	srv.enableTokenReview(tokenUsers{}, rbacRules{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()
	srv.Handler()(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, `Bearer realm="kardinal-bundle-api"`, w.Header().Get("Www-Authenticate"))
}
