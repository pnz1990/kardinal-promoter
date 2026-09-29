// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"crypto/subtle"
	"io/fs"
	"net/http"
	"strings"

	"github.com/rs/zerolog"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// newUIHandler builds the UI server handler: the /api/v1/ui/* API, the
// embedded React app at /ui/, authentication, a request body limit and CORS.
// assets may be nil when the embedded filesystem is unavailable.
func newUIHandler(k8s client.Client, assets fs.FS, auth uiAuthConfig, corsAllowedOrigins string, log zerolog.Logger) http.Handler {
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

	// Same-origin requests always pass; cross-origin requests pass only when
	// listed in --cors-allowed-origins ("*" allows all).
	return applyCORSMiddleware(handler, corsAllowedOrigins, log)
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
