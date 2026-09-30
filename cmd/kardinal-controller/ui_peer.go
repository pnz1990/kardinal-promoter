// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// uiPeerNotLocalMsg is the 403 body for an /api/ request from a non-loopback
// peer while no UI auth mode is set (#1262).
const uiPeerNotLocalMsg = "UI API: no UI auth mode is set, so only local clients " +
	"(kubectl port-forward) are served; set Helm value ui.auth.tokenReview=true " +
	"or ui.auth.tokenSecretRef.name (flags --ui-tokenreview-auth, --ui-auth-token)"

// requireLocalUIPeer refuses /api/ requests that did not come straight from
// loopback. It is installed only when no UI auth mode is set: the controller
// serves the UI API with its own ServiceAccount, so without authentication
// any client that reaches the port could act as the controller.
//
// kubectl port-forward reaches the pod from loopback: the container runtime
// dials localhost:<port> inside the pod's network namespace (containerd
// sandbox_portforward_linux.go, CRI-O PortForwardContainer). The Host header
// is client-set and proves nothing, so the check is on the TCP peer.
//
// A proxy in the pod (a service-mesh sidecar such as Istio or Linkerd) also
// connects from loopback, for any client in the mesh. Requests carrying the
// headers such proxies add are therefore treated as non-local; this is a
// best-effort signal, and the docs say to set an auth mode with a mesh.
func requireLocalUIPeer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && !isLocalUIPeer(r) {
			http.Error(w, uiPeerNotLocalMsg, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLocalUIPeer reports whether r arrived from a loopback address and carries
// no header that a forwarding proxy adds.
func isLocalUIPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Unmap().IsLoopback() {
		return false
	}
	return !hasProxyHeader(r.Header)
}

// hasProxyHeader reports whether h holds a header that a reverse proxy or a
// mesh sidecar adds: Forwarded, X-Forwarded-*, X-Real-Ip, X-Envoy-* (Envoy,
// Istio) or L5d-* (Linkerd).
func hasProxyHeader(h http.Header) bool {
	for k := range h {
		k = strings.ToLower(k)
		if k == "forwarded" || k == "x-real-ip" ||
			strings.HasPrefix(k, "x-forwarded-") ||
			strings.HasPrefix(k, "x-envoy-") ||
			strings.HasPrefix(k, "l5d-") {
			return true
		}
	}
	return false
}
