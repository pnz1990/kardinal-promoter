// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"crypto/subtle"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/accesslog"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/uiauth"
)

// maxUIRequestBody caps the body of /api/v1/ui/* requests. The largest real
// body (a rollback or gate-approve request) is well under 1 KiB.
const maxUIRequestBody = 1 << 20

// uiAuthConfig selects how the UI API authenticates and authorizes callers.
type uiAuthConfig struct {
	// staticToken, when set, is a shared bearer token required on every
	// /api/v1/ui/* request. It takes precedence over TokenReview (spec
	// issue-975 O4).
	staticToken string
	// tokens and access enable Kubernetes TokenReview authentication plus a
	// SubjectAccessReview for every object the UI API reads or writes on the
	// caller's behalf. main sets both or neither.
	tokens uiauth.TokenReviewer
	access uiauth.AccessReviewer
	// accessCheck answers POST /api/v1/ui/access only: access, rate-limited
	// per user and in total, under its own cache. Request authorization and
	// the Bundle API use access, which is not limited, so a flood of /access
	// calls cannot lock anyone out of reads and writes. Nil uses access.
	accessCheck uiauth.AccessReviewer
	// scopeNamespace is --watch-namespace: all-namespace reads are checked
	// against it because the controller cache only holds that namespace.
	scopeNamespace string
	// actors stamps the UI's writes with the verified caller (docs/actors.md).
	actors *actorWriter
	// freezeControls is --ui-freeze-controls: the UI may write freezes.
	freezeControls bool
	// receipts is --receipts: the steps API names each Verified step's
	// PromotionReceipt (docs/receipts.md).
	receipts bool
}

// reviewOptions are the TokenReview settings of the UI API and the Bundle
// API. The audiences are shared (--tokenreview-audiences); accepting the API
// server's own audience is decided per API (#1930):
// --tokenreview-accept-apiserver-audience for the UI API, which SSO needs,
// and --bundle-api-tokenreview-accept-apiserver-audience for the Bundle API.
type reviewOptions struct {
	audiences []string
	// acceptAPIServer is the UI API's opt-in.
	acceptAPIServer bool
	// bundleAcceptAPIServer is the Bundle API's own opt-in.
	bundleAcceptAPIServer bool
	// apiServerAudiences are the API server's own token audiences (from the
	// controller's ServiceAccount token); none of them may be in audiences.
	apiServerAudiences []string
	// shared, when set, makes every newReviewers call build on the same
	// reviewers, so the UI API and the Bundle API share one cache and one
	// rate limit. Each API's audience rule is applied after the cache
	// (uiauth.RequireAudience).
	shared *sharedReviewers
}

// reviewedAPI names the API a reviewer authenticates for.
type reviewedAPI string

const (
	reviewForUI      reviewedAPI = "the UI API"
	reviewForBundles reviewedAPI = "the Bundle API"
)

// acceptAPIServerFor is api's opt-in to tokens for the API server's audience.
func (o reviewOptions) acceptAPIServerFor(api reviewedAPI) bool {
	if api == reviewForBundles {
		return o.bundleAcceptAPIServer
	}
	return o.acceptAPIServer
}

// sharedReviewers builds the review clients once.
type sharedReviewers struct {
	once        sync.Once
	tokens      uiauth.TokenReviewer
	access      uiauth.AccessReviewer
	accessCheck uiauth.AccessReviewer
	err         error
}

// uiAuthFlags are the UI API auth flags.
type uiAuthFlags struct {
	staticToken string
	tokenReview bool
	// allowStaticWithTokenReview (--ui-auth-static-overrides-tokenreview)
	// accepts both modes set, the static token winning; without it that
	// combination is a startup error, since the static token silently turns
	// per-user RBAC off.
	allowStaticWithTokenReview bool
	review                     reviewOptions
	scopeNamespace             string
}

// buildUIAuth picks the UI API auth mode from the flags. The static token
// takes precedence (spec issue-975 O4) when both are allowed. In TokenReview
// mode an error building either review client is returned, so the caller can
// refuse to start rather than serve an open UI (C13b-design-09).
func buildUIAuth(cfg *rest.Config, f uiAuthFlags) (uiAuthConfig, error) {
	if f.staticToken != "" && f.tokenReview && !f.allowStaticWithTokenReview {
		return uiAuthConfig{}, fmt.Errorf("both --ui-auth-token and --ui-tokenreview-auth are set: the static token " +
			"would win and every UI caller would act as the controller; unset one, or set " +
			"--ui-auth-static-overrides-tokenreview (Helm ui.auth.allowStaticTokenWithTokenReview) to keep the static token")
	}
	auth := uiAuthConfig{staticToken: f.staticToken, scopeNamespace: f.scopeNamespace}
	if f.staticToken != "" || !f.tokenReview {
		return auth, nil
	}
	tokens, access, accessCheck, err := newReviewers(cfg, f.review, reviewForUI)
	if err != nil {
		return uiAuthConfig{}, err
	}
	auth.tokens, auth.access, auth.accessCheck = tokens, access, accessCheck
	return auth, nil
}

// newReviewers builds the cached TokenReview and SubjectAccessReview clients
// the UI API and the Bundle API authenticate and authorize callers with, for
// api: the token reviewer applies api's audience rule (#1930) over reviews
// shared by both APIs. TokenReviews that miss the cache are limited per
// client address. The third reviewer serves POST /api/v1/ui/access only: its
// SubjectAccessReviews are limited per user and in total, under a cache of
// its own, while access (for request authorization and the Bundle API) is
// not limited.
func newReviewers(cfg *rest.Config, opts reviewOptions, api reviewedAPI) (tokens uiauth.TokenReviewer, access, accessCheck uiauth.AccessReviewer, err error) {
	accept := opts.acceptAPIServerFor(api)
	if len(opts.audiences) == 0 && !accept {
		flag := "--tokenreview-accept-apiserver-audience (tokenReview.acceptAPIServerAudience)"
		if api == reviewForBundles {
			flag = "--bundle-api-tokenreview-accept-apiserver-audience (bundleAPI.acceptAPIServerAudience)"
		}
		return nil, nil, nil, fmt.Errorf("%s accepts no token: --tokenreview-audiences (tokenReview.audiences) is empty "+
			"and %s is off; set an audience such as %s", api, flag, uiauth.DefaultAudience)
	}
	base, access, accessCheck, err := newBaseReviewers(cfg, opts)
	if err != nil {
		return nil, nil, nil, err
	}
	return uiauth.RequireAudience(base, opts.audiences, accept), access, accessCheck, nil
}

// newBaseReviewers builds the reviewers of newReviewers before any API's
// audience rule: the token reviewer accepts the API server's audience when
// either API opts in. With opts.shared they are built once.
func newBaseReviewers(cfg *rest.Config, opts reviewOptions) (tokens uiauth.TokenReviewer, access, accessCheck uiauth.AccessReviewer, err error) {
	if s := opts.shared; s != nil {
		s.once.Do(func() {
			o := opts
			o.shared = nil
			s.tokens, s.access, s.accessCheck, s.err = newBaseReviewers(cfg, o)
		})
		return s.tokens, s.access, s.accessCheck, s.err
	}
	if err := uiauth.CheckAudiences(opts.audiences, opts.apiServerAudiences); err != nil {
		return nil, nil, nil, err
	}
	kubeTokens, err := uiauth.NewKubeTokenReviewer(cfg, opts.audiences, opts.acceptAPIServer || opts.bundleAcceptAPIServer)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("token reviewer: %w", err)
	}
	kubeAccess, err := uiauth.NewKubeAccessReviewer(cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("access reviewer: %w", err)
	}
	limited := uiauth.NewRateLimitedTokenReviewer(kubeTokens, uiauth.DefaultReviewsPerClientPerMinute, uiauth.DefaultReviewsPerMinute)
	return uiauth.NewCachedTokenReviewer(limited, uiauth.DefaultCacheTTL),
		uiauth.NewCachedAccessReviewer(kubeAccess, uiauth.DefaultCacheTTL),
		uiauth.NewCachedAccessReviewer(uiauth.NewRateLimitedAccessReviewer(kubeAccess,
			uiauth.DefaultAccessReviewsPerUserPerMinute, uiauth.DefaultAccessReviewsPerMinute), uiauth.DefaultCacheTTL), nil
}

// newUIHandler builds the UI server handler: the /api/v1/ui/* API, the
// embedded React app at /ui/, authentication, a request body limit, CORS and
// the security headers.
// assets may be nil when the embedded filesystem is unavailable. hosts is
// --ui-allowed-hosts; nil allows loopback only.
//
// apiReader, when not nil, is the uncached reader the lifecycle actions read
// the objects they write through (freshGets).
func newUIHandler(k8s client.Client, apiReader client.Reader, assets fs.FS, auth uiAuthConfig, corsAllowedOrigins string,
	hosts uiHostAllowlist, log zerolog.Logger) http.Handler {
	tokenReview := auth.staticToken == "" && auth.tokens != nil && auth.access != nil

	apiClient := k8s
	if tokenReview {
		apiClient = uiauth.NewAuthorizingClient(k8s, auth.access, auth.scopeNamespace)
	}
	uiMux := http.NewServeMux()
	apiServer := newUIAPIServer(apiClient, log)
	apiServer.scopeNamespace = auth.scopeNamespace
	apiServer.apiReader = apiReader
	apiServer.actors = auth.actors
	apiServer.freezeControls = auth.freezeControls
	apiServer.receipts = auth.receipts
	if tokenReview {
		apiServer.accessCheck = auth.accessCheck
	}
	apiServer.RegisterRoutes(uiMux)
	if assets != nil {
		uiMux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(noDirListingFS{assets}))))
	}

	var handler http.Handler = uiMux
	switch {
	case auth.staticToken != "":
		handler = staticTokenMiddleware(uiMux, auth.staticToken)
	case tokenReview:
		handler = uiauth.Middleware(uiMux, auth.tokens)
	}
	handler = limitUIRequestBody(handler)

	// Same-origin requests to an allowed Host pass; cross-origin requests pass
	// only when listed in --cors-allowed-origins ("*" allows all). With auth
	// off, every /api/ request to any other Host is refused (DNS rebinding).
	authEnabled := auth.staticToken != "" || tokenReview
	handler = applyCORSMiddleware(handler, corsAllowedOrigins, hosts, authEnabled, log)
	// With auth off, /api/ answers loopback peers only (kubectl port-forward),
	// whatever the Host header says (#1262).
	if !authEnabled {
		handler = requireLocalUIPeer(handler)
	}
	// Anti-framing, CSP and nosniff on every UI response, refusals included
	// (C10b-web-09).
	return withUISecurityHeaders(handler)
}

// staticTokenMiddleware requires "Authorization: Bearer <token>" on
// /api/v1/ui/*. Static assets at /ui/* are public (they hold no data).
func staticTokenMiddleware(next http.Handler, token string) http.Handler {
	tokenBytes := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/ui/") {
			authHeader := r.Header.Get("Authorization")
			provided := strings.TrimPrefix(authHeader, "Bearer ")
			// Constant-time comparison prevents timing attacks.
			if !strings.HasPrefix(authHeader, "Bearer ") ||
				subtle.ConstantTimeCompare([]byte(provided), tokenBytes) != 1 {
				w.Header().Set("Www-Authenticate", `Bearer realm="kardinal-ui"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			accesslog.FromContext(r.Context()).Auth = "static-token"
		}
		next.ServeHTTP(w, r)
	})
}

// limitUIRequestBody caps /api/v1/ui/* request bodies at maxUIRequestBody.
// Handlers then fail to decode an oversized body and answer 400.
func limitUIRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && strings.HasPrefix(r.URL.Path, "/api/v1/ui/") {
			r.Body = http.MaxBytesReader(w, r.Body, maxUIRequestBody)
		}
		next.ServeHTTP(w, r)
	})
}

// noDirListingFS hides every directory except the root, so http.FileServer
// serves files and index.html but never lists a directory such as
// /ui/assets/.
type noDirListingFS struct{ fs.FS }

func (f noDirListingFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil || name == "." {
		return file, err
	}
	st, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if st.IsDir() {
		_ = file.Close()
		return nil, fs.ErrNotExist
	}
	return file, nil
}
