// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/accesslog"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

func accessLines(t *testing.T, buf *bytes.Buffer) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(l), &m), l)
		out = append(out, m)
	}
	return out
}

// TestAccessLog_UIAPI runs the real UI handler in TokenReview mode behind the
// access log, as main wires it: a token's first request is a login (its
// TokenReview), later ones are served from the cache and not logged, a
// refused action is logged with its reason, a write is logged with the user,
// and a bad token is a denial. No token appears in the log.
func TestAccessLog_UIAPI(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"}},
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-b"}}).Build()
	tokens := &uiTestTokens{users: map[string]string{"deployer-s3cret": "system:serviceaccount:team-a:deployer"}}
	access := &uiTestAccess{rules: map[string]map[string][]string{
		"system:serviceaccount:team-a:deployer": {"*": {"get", "list"}, "team-a": {"get", "list", "update"}},
	}}
	var buf bytes.Buffer
	log := accesslog.New(accesslog.Config{}, zerolog.New(&buf))
	h := log.Middleware("ui", newUIHandler(c, nil, uiAuthConfig{
		tokens: uiauth.NewCachedTokenReviewer(tokens, time.Minute), access: access}, "", nil, zerolog.Nop()))

	require.Equal(t, http.StatusOK, uiAuthDo(t, h, http.MethodGet, "/api/v1/ui/pipelines", "Bearer deployer-s3cret", "").Code)
	require.Equal(t, http.StatusOK, uiAuthDo(t, h, http.MethodGet, "/api/v1/ui/pipelines", "Bearer deployer-s3cret", "").Code)
	rec := uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/pause", "Bearer deployer-s3cret", `{"pipeline":"app","namespace":"team-a"}`)
	require.Equal(t, http.StatusOK, rec.Code, "pause: %s", rec.Body.String())
	rec = uiAuthDo(t, h, http.MethodPost, "/api/v1/ui/pause", "Bearer deployer-s3cret", `{"pipeline":"app","namespace":"team-b"}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, http.StatusUnauthorized, uiAuthDo(t, h, http.MethodGet, "/api/v1/ui/pipelines", "Bearer guess-s3cret", "").Code)

	got := accessLines(t, &buf)
	require.Len(t, got, 4, buf.String())
	user := "system:serviceaccount:team-a:deployer"
	want := []map[string]interface{}{
		{"access": "login", "method": "GET", "status": float64(200), "user": user, "auth": "tokenreview"},
		{"access": "write", "method": "POST", "path": "/api/v1/ui/pause", "status": float64(200), "user": user},
		{"access": "denied", "status": float64(403), "user": user},
		{"access": "denied", "status": float64(401), "reason": "unauthorized"},
	}
	for i, w := range want {
		for k, v := range w {
			assert.Equal(t, v, got[i][k], "line %d %s", i, k)
		}
		assert.Equal(t, "ui", got[i]["server"])
	}
	assert.Contains(t, got[2]["reason"], "cannot", "the refusal's reason")
	assert.NotContains(t, buf.String(), "s3cret")
}

// TestAccessLog_BundleAPIStaticToken: a Bundle created with the static token
// is a write by static-token; a wrong token is a denial; neither logs a token.
func TestAccessLog_BundleAPIStaticToken(t *testing.T) {
	var buf bytes.Buffer
	log := accesslog.New(accesslog.Config{}, zerolog.New(&buf))
	h := log.Middleware("bundle-api", newBundleAPIServer(bundleAPIClient(), "static-s3cret", "default").Handler())
	for _, token := range []string{"static-s3cret", "wrong-s3cret"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles",
			strings.NewReader(`{"pipeline":"app","namespace":"default","type":"image","images":[{"repository":"r","tag":"1"}]}`))
		req.Header.Set("Authorization", "Bearer "+token)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	got := accessLines(t, &buf)
	require.Len(t, got, 2, buf.String())
	assert.Equal(t, "write", got[0]["access"])
	assert.Equal(t, "static-token", got[0]["auth"])
	assert.Equal(t, "denied", got[1]["access"])
	assert.Equal(t, float64(401), got[1]["status"])
	assert.NotContains(t, buf.String(), "s3cret")
}
