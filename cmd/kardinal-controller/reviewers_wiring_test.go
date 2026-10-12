// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/rest"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

// fakeReviewAPI is an API server that answers every TokenReview with
// "not authenticated" and counts them.
func fakeReviewAPI(t *testing.T) (*rest.Config, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tokenreviews") {
			http.NotFound(w, r)
			return
		}
		n.Add(1)
		var tr authv1.TokenReview
		_ = json.NewDecoder(r.Body).Decode(&tr)
		tr.Status = authv1.TokenReviewStatus{Authenticated: false}
		tr.APIVersion, tr.Kind = "authentication.k8s.io/v1", "TokenReview"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&tr)
	}))
	t.Cleanup(srv.Close)
	return &rest.Config{Host: srv.URL}, &n
}

// TestReviewers_RateLimitIsWiredAndShared: the reviewers main builds are
// rate limited per client address before a TokenReview is sent, and the UI
// API and the Bundle API share them: once one client used its budget on the
// UI, its Bundle API requests get 429 too, and no more reviews reach the API
// server.
func TestReviewers_RateLimitIsWiredAndShared(t *testing.T) {
	cfg, reviews := fakeReviewAPI(t)
	review := reviewOptions{audiences: []string{uiauth.DefaultAudience}, shared: &sharedReviewers{}}
	auth, err := buildUIAuth(cfg, uiAuthFlags{tokenReview: true, review: review})
	require.NoError(t, err)
	ui := newUIHandler(bundleAPIClient(), nil, auth, "", nil, zerolog.Nop())
	tokens, access, _, err := newReviewers(cfg, review)
	require.NoError(t, err)
	assert.Same(t, auth.tokens, tokens, "one reviewer for both APIs")
	bundles := newBundleAPIServer(bundleAPIClient(), "", "default")
	bundles.enableTokenReview(tokens, access)

	for i := 0; i < uiauth.DefaultReviewsPerClientPerMinute; i++ {
		rec := uiAuthDo(t, ui, http.MethodGet, "/api/v1/ui/pipelines", fmt.Sprintf("Bearer guess-%d", i), "")
		require.Equal(t, http.StatusUnauthorized, rec.Code, "guess %d", i)
	}
	assert.Equal(t, int32(uiauth.DefaultReviewsPerClientPerMinute), reviews.Load())

	rec := uiAuthDo(t, ui, http.MethodGet, "/api/v1/ui/pipelines", "Bearer one-more", "")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:54321" // the address uiAuthDo uses
	req.Header.Set("Authorization", "Bearer bundle-guess")
	w := httptest.NewRecorder()
	bundles.Handler()(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code, "the Bundle API shares the budget")
	assert.Equal(t, int32(uiauth.DefaultReviewsPerClientPerMinute), reviews.Load(), "no review sent past the limit")
}

// TestNewReviewers_RefusesAPIServerAudience: an API server audience in
// tokenReview.audiences stops the controller, pointing to the opt-in.
func TestNewReviewers_RefusesAPIServerAudience(t *testing.T) {
	cfg, _ := fakeReviewAPI(t)
	_, _, _, err := newReviewers(cfg, reviewOptions{
		audiences:          []string{uiauth.DefaultAudience, "https://kubernetes.default.svc"},
		apiServerAudiences: uiauth.APIServerAudiences("/nonexistent"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--tokenreview-accept-apiserver-audience")
}

// fakeKubeAuthAPI is an API server that authenticates "tok-<name>" as user
// <name> and allows every SubjectAccessReview, counting them.
func fakeKubeAuthAPI(t *testing.T) (*rest.Config, *atomic.Int32) {
	t.Helper()
	var sars atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/tokenreviews"):
			var tr authv1.TokenReview
			_ = json.NewDecoder(r.Body).Decode(&tr)
			name, ok := strings.CutPrefix(tr.Spec.Token, "tok-")
			tr.Status = authv1.TokenReviewStatus{Authenticated: ok, User: authv1.UserInfo{Username: name},
				Audiences: tr.Spec.Audiences}
			tr.APIVersion, tr.Kind = "authentication.k8s.io/v1", "TokenReview"
			_ = json.NewEncoder(w).Encode(&tr)
		case strings.HasSuffix(r.URL.Path, "/subjectaccessreviews"):
			sars.Add(1)
			var sar authzv1.SubjectAccessReview
			_ = json.NewDecoder(r.Body).Decode(&sar)
			sar.Status = authzv1.SubjectAccessReviewStatus{Allowed: true}
			sar.APIVersion, sar.Kind = "authorization.k8s.io/v1", "SubjectAccessReview"
			_ = json.NewEncoder(w).Encode(&sar)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	// JSON, so the fake can read the reviews (client-go sends protobuf by
	// default); no client-side rate limit, so the flood fits in one minute.
	return &rest.Config{Host: srv.URL, QPS: -1, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}, &sars
}

// TestReviewers_AccessFloodLeavesOthersAlone: the rate limit on POST
// /api/v1/ui/access is its own. Ten users flooding it with fresh names use
// their budgets and then the budget for everyone (each 429 says which), and a
// different user's UI reads and Bundle API calls, which authorize through the
// unlimited reviewer, still succeed (QA #1748).
//
// Covers UI-ACCESS-02.
func TestReviewers_AccessFloodLeavesOthersAlone(t *testing.T) {
	cfg, sars := fakeKubeAuthAPI(t)
	review := reviewOptions{audiences: []string{uiauth.DefaultAudience}, shared: &sharedReviewers{}}
	auth, err := buildUIAuth(cfg, uiAuthFlags{tokenReview: true, review: review})
	require.NoError(t, err)
	ui := newUIHandler(bundleAPIClient(), nil, auth, "", nil, zerolog.Nop())
	tokens, access, _, err := newReviewers(cfg, review)
	require.NoError(t, err)
	bundles := newBundleAPIServer(bundleAPIClient(), "", "default")
	bundles.enableTokenReview(tokens, access)

	perUser := uiauth.DefaultAccessReviewsPerUserPerMinute
	users := uiauth.DefaultAccessReviewsPerMinute / perUser
	require.Equal(t, 10, users, "ten users use the budget for everyone")
	flood := func(user string, round int) *httptest.ResponseRecorder {
		checks := make([]string, maxAccessChecks)
		for i := range checks {
			checks[i] = fmt.Sprintf(`{"verb":"update","resource":"pipelines","subresource":"pause","name":"%s-%d-%d"}`, user, round, i)
		}
		return uiAuthDo(t, ui, http.MethodPost, "/api/v1/ui/access", "Bearer tok-"+user, `{"checks":[`+strings.Join(checks, ",")+`]}`)
	}
	for u := 0; u < users; u++ {
		user := fmt.Sprintf("flooder-%d", u)
		var last *httptest.ResponseRecorder
		for round := 0; round <= perUser/maxAccessChecks+1; round++ {
			last = flood(user, round)
		}
		require.Equal(t, http.StatusTooManyRequests, last.Code, last.Body.String())
		assert.Contains(t, last.Body.String(), "for this user")
	}
	assert.Equal(t, int32(uiauth.DefaultAccessReviewsPerMinute), sars.Load(), "no review sent past the limits")

	rec := flood("other", 0)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "from all users together")

	rec = uiAuthDo(t, ui, http.MethodGet, "/api/v1/ui/pipelines", "Bearer tok-other", "")
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(
		`{"pipeline":"app","namespace":"team-a","type":"image","images":[{"repository":"r","tag":"1"}]}`))
	req.Header.Set("Authorization", "Bearer tok-other")
	w := httptest.NewRecorder()
	bundles.Handler()(w, req)
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}
