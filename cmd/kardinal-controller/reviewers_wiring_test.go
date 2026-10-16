// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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
	ui := newUIHandler(bundleAPIClient(), nil, nil, auth, "", nil, zerolog.Nop())
	tokens, access, _, err := newReviewers(cfg, review, reviewForBundles)
	require.NoError(t, err)
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
	}, reviewForUI)
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
	ui := newUIHandler(bundleAPIClient(), nil, nil, auth, "", nil, zerolog.Nop())
	tokens, access, _, err := newReviewers(cfg, review, reviewForBundles)
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

// fakeAudienceAPI is an API server that authenticates tokens the way the
// real one does for the three callers of #1930, and allows every
// SubjectAccessReview:
//   - "pod-default": a Pod's default ServiceAccount token. It is for the API
//     server's audience, so only a review without spec.audiences
//     authenticates it, and the review names that audience.
//   - "oidc-alice": an OIDC ID token, from an authenticator that is not
//     audience-aware: a review for kardinal-promoter refuses it, and a
//     review without spec.audiences authenticates it naming no audience.
//   - "ci": a ServiceAccount token minted for kardinal-promoter.
func fakeAudienceAPI(t *testing.T) *rest.Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/tokenreviews"):
			var tr authv1.TokenReview
			_ = json.NewDecoder(r.Body).Decode(&tr)
			forAPIServer := len(tr.Spec.Audiences) == 0
			switch {
			case tr.Spec.Token == "pod-default" && forAPIServer:
				tr.Status = authv1.TokenReviewStatus{Authenticated: true, Audiences: []string{"https://kubernetes.default.svc"},
					User: authv1.UserInfo{Username: "system:serviceaccount:team-a:default",
						Groups: []string{"system:serviceaccounts", "system:serviceaccounts:team-a", "system:authenticated"}}}
			case tr.Spec.Token == "oidc-alice" && forAPIServer:
				tr.Status = authv1.TokenReviewStatus{Authenticated: true,
					User: authv1.UserInfo{Username: "oidc:alice", Groups: []string{"oidc:release-managers", "system:authenticated"}}}
			case tr.Spec.Token == "ci" && slices.Contains(tr.Spec.Audiences, uiauth.DefaultAudience):
				tr.Status = authv1.TokenReviewStatus{Authenticated: true, Audiences: []string{uiauth.DefaultAudience},
					User: authv1.UserInfo{Username: "system:serviceaccount:team-a:ci"}}
			}
			tr.APIVersion, tr.Kind = "authentication.k8s.io/v1", "TokenReview"
			_ = json.NewEncoder(w).Encode(&tr)
		case strings.HasSuffix(r.URL.Path, "/subjectaccessreviews"):
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
	return &rest.Config{Host: srv.URL, QPS: -1, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}
}

// TestReviewers_AudienceIsPerAPI (#1930): each API's opt-in to the API
// server's audience applies to that API only, though both share one
// TokenReview cache: the UI's (SSO) does not reach the Bundle API, and the
// Bundle API's own, set alone, admits an OIDC token there while the UI
// still refuses it. A Pod's default ServiceAccount token is refused by both
// whatever the opt-ins. Each 401 names the rule that refused it: the API's
// opt-in is off, or the token is a ServiceAccount's.
func TestReviewers_AudienceIsPerAPI(t *testing.T) {
	const bundle = `{"pipeline":"app","namespace":"team-a","type":"image","images":[{"repository":"r","tag":"1"}]}`
	postBundle := func(h http.HandlerFunc, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/bundles", strings.NewReader(bundle))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h(w, req)
		return w
	}
	// The outcomes: a status code, or a 401 and the rule that refused it.
	const (
		noOptIn = "401: this API does not accept the API server's audience"
		saToken = "401: a ServiceAccount token for the API server's audience"
	)
	reasons := map[string]string{noOptIn: "does not accept the API server's", saToken: "a ServiceAccount token for the API server's audience is refused"}
	tests := []struct {
		name                 string
		uiOptIn, bundleOptIn bool
		uiWant, bundlesWant  map[string]string
	}{
		{"SSO on the UI only", true, false,
			map[string]string{"oidc-alice": "200", "pod-default": saToken, "ci": "200"},
			map[string]string{"oidc-alice": noOptIn, "pod-default": noOptIn, "ci": "201"}},
		{"the Bundle API opts in alone", false, true,
			map[string]string{"oidc-alice": noOptIn, "pod-default": noOptIn, "ci": "200"},
			map[string]string{"oidc-alice": "201", "pod-default": saToken, "ci": "201"}},
		{"both opt in", true, true,
			map[string]string{"oidc-alice": "200", "pod-default": saToken, "ci": "200"},
			map[string]string{"oidc-alice": "201", "pod-default": saToken, "ci": "201"}},
	}
	check := func(t *testing.T, api, tok, want string, rec *httptest.ResponseRecorder) {
		t.Helper()
		reason, refused := reasons[want]
		if !refused {
			assert.Equal(t, want, fmt.Sprint(rec.Code), "%s, %s: %s", api, tok, rec.Body.String())
			return
		}
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s, %s: %s", api, tok, rec.Body.String())
		assert.Contains(t, rec.Body.String(), reason, "%s, %s: the 401 names the rule", api, tok)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := fakeAudienceAPI(t)
			review := reviewOptions{audiences: []string{uiauth.DefaultAudience}, acceptAPIServer: tt.uiOptIn,
				bundleAcceptAPIServer: tt.bundleOptIn, shared: &sharedReviewers{}}
			auth, err := buildUIAuth(cfg, uiAuthFlags{tokenReview: true, review: review})
			require.NoError(t, err)
			ui := newUIHandler(bundleAPIClient(), nil, nil, auth, "", nil, zerolog.Nop())
			tokens, access, _, err := newReviewers(cfg, review, reviewForBundles)
			require.NoError(t, err)
			bundles := newBundleAPIServer(bundleAPIClient(), "", "default")
			bundles.enableTokenReview(tokens, access)

			// One API first, so each token's review is in the shared cache
			// when the other sees it; then the other way round.
			for _, tok := range []string{"oidc-alice", "pod-default", "ci"} {
				check(t, "UI API", tok, tt.uiWant[tok], uiAuthDo(t, ui, http.MethodGet, "/api/v1/ui/pipelines", "Bearer "+tok, ""))
			}
			for _, tok := range []string{"oidc-alice", "pod-default", "ci"} {
				check(t, "Bundle API", tok, tt.bundlesWant[tok], postBundle(bundles.Handler(), tok))
			}
			for _, tok := range []string{"oidc-alice", "pod-default", "ci"} {
				check(t, "UI API, after the Bundle API", tok, tt.uiWant[tok],
					uiAuthDo(t, ui, http.MethodGet, "/api/v1/ui/pipelines", "Bearer "+tok, ""))
			}
		})
	}
}

// TestNewReviewers_NoAudienceNeedsThatAPIsOptIn: with no audience
// configured, an API without its own opt-in to the API server's audience
// would accept no token; the controller refuses to start and names the
// API's flag, even when the other API opted in.
func TestNewReviewers_NoAudienceNeedsThatAPIsOptIn(t *testing.T) {
	cfg, _ := fakeReviewAPI(t)
	_, _, _, err := newReviewers(cfg, reviewOptions{acceptAPIServer: true}, reviewForBundles)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--bundle-api-tokenreview-accept-apiserver-audience")
	_, _, _, err = newReviewers(cfg, reviewOptions{bundleAcceptAPIServer: true}, reviewForUI)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--tokenreview-accept-apiserver-audience")
	_, _, _, err = newReviewers(cfg, reviewOptions{acceptAPIServer: true}, reviewForUI)
	require.NoError(t, err)
}
