// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// basePathSegment is one segment of --ui-base-path: URL-safe characters
// only, so the prefix needs no escaping in a path or a Location header.
var basePathSegment = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// parseUIBasePath validates --ui-base-path and returns it without a trailing
// slash ("" for none). The prefix must start with "/", may have several
// segments ("/tools/kardinal") and must not start with a segment the UI server
// serves at the root (ui, api): those requests would be ambiguous.
func parseUIBasePath(raw string) (string, error) {
	p := strings.TrimRight(strings.TrimSpace(raw), "/")
	if p == "" {
		return "", nil
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("--ui-base-path %q: must start with /", raw)
	}
	segs := strings.Split(p[1:], "/")
	for _, s := range segs {
		if s == "." || s == ".." || !basePathSegment.MatchString(s) {
			return "", fmt.Errorf("--ui-base-path %q: each segment must be letters, digits, '.', '_', '~' or '-' (and not . or ..)", raw)
		}
	}
	if segs[0] == "ui" || segs[0] == "api" {
		return "", fmt.Errorf("--ui-base-path %q: must not start with /%s, which the UI server already serves", raw, segs[0])
	}
	return p, nil
}

// withUIBasePath serves the UI and its API under base as well as at the
// root: <base>/ui/ and <base>/api/v1/ui/* reach next with base removed, so
// authentication, CORS, the loopback check and the routes see the same paths
// as without a base. <base> and <base>/ui redirect to <base>/ui/, which the
// relative asset URLs of the built UI need. Every other path, including the
// unprefixed /ui/ and /api/v1/ui/* (kubectl port-forward), passes unchanged.
// With base "" it returns next.
func withUIBasePath(base string, next http.Handler) http.Handler {
	if base == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch rest, ok := strings.CutPrefix(r.URL.Path, base); {
		case !ok:
			next.ServeHTTP(w, r)
		case rest == "" || rest == "/" || rest == "/ui":
			for k, v := range uiSecurityHeaders {
				w.Header().Set(k, v)
			}
			http.Redirect(w, r, base+"/ui/", http.StatusFound)
		case strings.HasPrefix(rest, "/ui/") || strings.HasPrefix(rest, "/api/v1/ui/"):
			http.StripPrefix(base, next).ServeHTTP(w, r)
		default:
			// /<base>x or <base>/other: not the UI's.
			next.ServeHTTP(w, r)
		}
	})
}
