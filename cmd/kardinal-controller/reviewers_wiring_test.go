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
	tokens, access, err := newReviewers(cfg, review)
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
	_, _, err := newReviewers(cfg, reviewOptions{
		audiences:          []string{uiauth.DefaultAudience, "https://kubernetes.default.svc"},
		apiServerAudiences: uiauth.APIServerAudiences("/nonexistent"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--tokenreview-accept-apiserver-audience")
}
