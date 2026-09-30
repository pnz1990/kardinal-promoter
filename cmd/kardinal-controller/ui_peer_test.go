// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestUIHandler_NoAuthServesLoopbackPeersOnly covers #1262: with no UI auth
// mode set, /api/ answers only requests whose TCP peer is loopback (kubectl
// port-forward) and that carry no proxy header. The Host header is set to
// localhost in every case, since any client can send it.
func TestUIHandler_NoAuthServesLoopbackPeersOnly(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		path     string
		peer     string
		header   map[string]string
		wantCode int
	}{
		{name: "port-forward ipv4", peer: "127.0.0.1:41000", wantCode: http.StatusOK},
		{name: "port-forward ipv6", peer: "[::1]:41000", wantCode: http.StatusOK},
		{name: "ipv4-mapped loopback", peer: "[::ffff:127.0.0.1]:41000", wantCode: http.StatusOK},
		{name: "other loopback address", peer: "127.0.0.6:41000", wantCode: http.StatusOK},
		{name: "pod in the cluster", peer: "10.244.1.7:41000", wantCode: http.StatusForbidden},
		{name: "node or NodePort client", peer: "192.168.49.2:41000", wantCode: http.StatusForbidden},
		{name: "public ipv6", peer: "[2001:db8::7]:41000", wantCode: http.StatusForbidden},
		{name: "unparseable peer", peer: "", wantCode: http.StatusForbidden},
		{name: "write from a pod", method: http.MethodPost, path: "/api/v1/ui/pause",
			peer: "10.244.1.7:41000", wantCode: http.StatusForbidden},
		// ServeMux redirects to the clean /api/ path, which is then checked.
		{name: "dot segments from a pod", path: "/ui/../api/v1/ui/pipelines",
			peer: "10.244.1.7:41000", wantCode: http.StatusTemporaryRedirect},
		{name: "static assets stay public", path: "/ui/", peer: "10.244.1.7:41000", wantCode: http.StatusOK},
		// A mesh sidecar or reverse proxy in the pod connects from loopback for
		// clients anywhere in the mesh; the headers it adds make it non-local.
		{name: "X-Forwarded-For", peer: "127.0.0.1:41000",
			header: map[string]string{"X-Forwarded-For": "10.244.1.7"}, wantCode: http.StatusForbidden},
		{name: "X-Forwarded-Proto", peer: "127.0.0.1:41000",
			header: map[string]string{"X-Forwarded-Proto": "http"}, wantCode: http.StatusForbidden},
		{name: "Forwarded", peer: "127.0.0.1:41000",
			header: map[string]string{"Forwarded": "for=10.244.1.7"}, wantCode: http.StatusForbidden},
		{name: "X-Real-Ip", peer: "127.0.0.1:41000",
			header: map[string]string{"X-Real-Ip": "10.244.1.7"}, wantCode: http.StatusForbidden},
		{name: "Istio sidecar", peer: "127.0.0.6:41000",
			header: map[string]string{"X-Envoy-Attempt-Count": "1"}, wantCode: http.StatusForbidden},
		{name: "Linkerd proxy", peer: "127.0.0.1:41000",
			header:   map[string]string{"L5d-Client-Id": "default.default.serviceaccount.identity.linkerd.cluster.local"},
			wantCode: http.StatusForbidden},
		{name: "unrelated header", peer: "127.0.0.1:41000",
			header: map[string]string{"X-Request-Id": "abc"}, wantCode: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(uiScheme()).WithObjects(
				&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"}},
			).Build()
			h := newUIHandler(c, uiTestAssets, uiAuthConfig{}, "", nil, zerolog.Nop())

			method, path, body := tt.method, tt.path, ""
			if method == "" {
				method = http.MethodGet
			}
			if path == "" {
				path = "/api/v1/ui/pipelines"
			}
			if method == http.MethodPost {
				body = `{"pipeline":"app"}`
			}
			req := httptest.NewRequest(method, path, strings.NewReader(body))
			req.Host = "localhost:8082"
			req.RemoteAddr = tt.peer
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			for k, v := range tt.header {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
			if tt.wantCode == http.StatusForbidden {
				assert.Contains(t, rec.Body.String(), "ui.auth.tokenReview")
				assert.Contains(t, rec.Body.String(), "ui.auth.tokenSecretRef")
			}
			// Security headers apply to the refusal too.
			assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))

			var pl v1alpha1.Pipeline
			require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "app"}, &pl))
			assert.False(t, pl.Spec.Paused, "a refused request must not pause the pipeline")
		})
	}
}

// TestHasProxyHeader_Lowercase covers header maps built without
// canonicalisation (HTTP/2 and direct map writes keep lower-case keys).
func TestHasProxyHeader_Lowercase(t *testing.T) {
	for _, k := range []string{"x-envoy-peer-metadata", "l5d-dst-canonical", "x-forwarded-client-cert", "forwarded"} {
		assert.True(t, hasProxyHeader(http.Header{k: {"v"}}), k)
	}
	assert.False(t, hasProxyHeader(http.Header{"Authorization": {"Bearer x"}, "Origin": {"http://localhost:8082"}}))
}

// TestUIHandler_AuthModeServesAnyPeer: with an auth mode set, the peer check
// is off; authentication decides.
func TestUIHandler_AuthModeServesAnyPeer(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	h := newUIHandler(c, nil, uiAuthConfig{staticToken: "s3cret"}, "", nil, zerolog.Nop())

	for token, want := range map[string]int{"Bearer s3cret": http.StatusOK, "": http.StatusUnauthorized} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/pipelines", nil)
		req.Host = "localhost:8082"
		req.RemoteAddr = "10.244.1.7:41000"
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, want, rec.Code, rec.Body.String())
	}
}

// TestUIHandler_NoAuthRealSockets serves the handler on a real listener. The
// port-forward case dials exactly as containerd's CRI port-forward does
// (net.Dial("tcp4", "localhost:<port>") from inside the pod netns) and is
// served. A client on a non-loopback address of this host is refused.
func TestUIHandler_NoAuthRealSockets(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(uiScheme()).Build()
	srv := httptest.NewUnstartedServer(newUIHandler(c, nil, uiAuthConfig{}, "", nil, zerolog.Nop()))
	require.NoError(t, srv.Listener.Close())
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	require.NoError(t, err)
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)

	get := func(t *testing.T, network, addr string) int {
		t.Helper()
		conn, err := net.DialTimeout(network, addr, 5*time.Second)
		require.NoError(t, err)
		defer conn.Close() //nolint:errcheck
		require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
		_, err = fmt.Fprintf(conn, "GET /api/v1/ui/pipelines HTTP/1.1\r\nHost: localhost:8082\r\nConnection: close\r\n\r\n")
		require.NoError(t, err)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	t.Run("port-forward (containerd dial)", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, get(t, "tcp4", net.JoinHostPort("localhost", port)))
	})
	t.Run("non-loopback client", func(t *testing.T) {
		ip, ok := hostIPv4()
		if !ok {
			t.Skip("no non-loopback IPv4 address on this host")
		}
		assert.Equal(t, http.StatusForbidden, get(t, "tcp4", net.JoinHostPort(ip.String(), port)))
	})
}

// hostIPv4 returns a non-loopback IPv4 address of this host.
func hostIPv4() (netip.Addr, bool) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return netip.Addr{}, false
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipnet.IP)
		if ip = ip.Unmap(); ok && ip.Is4() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
			return ip, true
		}
	}
	return netip.Addr{}, false
}
