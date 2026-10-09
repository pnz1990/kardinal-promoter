// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"crypto/subtle"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

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
	// scopeNamespace is --watch-namespace: all-namespace reads are checked
	// against it because the controller cache only holds that namespace.
	scopeNamespace string
}

// buildUIAuth picks the UI API auth mode from the flags. The static token
// takes precedence (spec issue-975 O4). In TokenReview mode an error building
// either review client is returned, so the caller can refuse to start rather
// than serve an open UI (C13b-design-09).
func buildUIAuth(cfg *rest.Config, staticToken string, tokenReview bool, scopeNamespace string) (uiAuthConfig, error) {
	auth := uiAuthConfig{staticToken: staticToken, scopeNamespace: scopeNamespace}
	if staticToken != "" || !tokenReview {
		return auth, nil
	}
	tokens, err := uiauth.NewKubeTokenReviewer(cfg)
	if err != nil {
		return uiAuthConfig{}, fmt.Errorf("token reviewer: %w", err)
	}
	access, err := uiauth.NewKubeAccessReviewer(cfg)
	if err != nil {
		return uiAuthConfig{}, fmt.Errorf("access reviewer: %w", err)
	}
	auth.tokens = uiauth.NewCachedTokenReviewer(tokens, uiauth.DefaultCacheTTL)
	auth.access = uiauth.NewCachedAccessReviewer(access, uiauth.DefaultCacheTTL)
	return auth, nil
}

// newUIHandler builds the UI server handler: the /api/v1/ui/* API, the
// embedded React app at /ui/, authentication, a request body limit, CORS and
// the security headers.
// assets may be nil when the embedded filesystem is unavailable. hosts is
// --ui-allowed-hosts; nil allows loopback only.
func newUIHandler(k8s client.Client, assets fs.FS, auth uiAuthConfig, corsAllowedOrigins string,
	hosts uiHostAllowlist, log zerolog.Logger) http.Handler {
	tokenReview := auth.staticToken == "" && auth.tokens != nil && auth.access != nil

	apiClient := k8s
	if tokenReview {
		apiClient = uiauth.NewAuthorizingClient(k8s, auth.access, auth.scopeNamespace)
	}
	uiMux := http.NewServeMux()
	newUIAPIServer(apiClient, log).RegisterRoutes(uiMux)
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
