// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package egress_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
)

func TestCheckAddr(t *testing.T) {
	tests := []struct {
		addr    string
		blocked bool
	}{
		// Loopback: the UI API trusts loopback peers when no auth mode is set.
		{"127.0.0.1", true},
		{"127.1.2.3", true},
		{"::1", true},
		{"::ffff:127.0.0.1", true},
		// Unspecified and "this network".
		{"0.0.0.0", true},
		{"0.1.2.3", true},
		{"::", true},
		// Link-local and the metadata endpoints in it.
		{"169.254.169.254", true},
		{"169.254.170.2", true},
		{"169.254.170.23", true},
		{"::ffff:169.254.169.254", true},
		{"fe80::1", true},
		// Metadata outside link-local.
		{"fd00:ec2::254", true},
		{"fd00:ec2::23", true},
		{"fd20:ce::254", true},
		{"100.100.100.200", true},
		{"168.63.129.16", true},
		// Multicast.
		{"224.0.0.1", true},
		{"239.1.1.1", true},
		{"ff02::1", true},
		{"ff01::1", true},
		// Allowed: private ranges (in-cluster Services, Prometheus) and public.
		{"10.96.0.10", false},
		{"172.16.5.4", false},
		{"192.168.1.10", false},
		{"100.64.0.1", false},
		{"fd00:ec2::1", false},
		{"fc00::1", false},
		{"8.8.8.8", false},
		{"2606:4700:4700::1111", false},
		{"::ffff:10.0.0.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			err := egress.CheckAddr(netip.MustParseAddr(tt.addr))
			if tt.blocked {
				require.Error(t, err)
				assert.ErrorIs(t, err, egress.ErrBlockedAddress)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCheckAddr_Invalid(t *testing.T) {
	assert.ErrorIs(t, egress.CheckAddr(netip.Addr{}), egress.ErrBlockedAddress)
}

func TestControl(t *testing.T) {
	tests := []struct {
		name    string
		address string
		blocked bool
	}{
		{"ipv4 loopback", "127.0.0.1:8082", true},
		{"ipv6 loopback", "[::1]:8082", true},
		{"metadata", "169.254.169.254:80", true},
		{"link-local with zone", "[fe80::1%eth0]:80", true},
		{"cluster service", "10.96.0.10:9090", false},
		{"public ipv6", "[2606:4700:4700::1111]:443", false},
		// The dialer always passes a resolved IP; anything else fails closed.
		{"hostname", "localhost:80", true},
		{"no port", "10.0.0.1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := egress.Control("tcp", tt.address, nil)
			if tt.blocked {
				assert.ErrorIs(t, err, egress.ErrBlockedAddress)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestNewTransport_RefusesLoopbackServer proves the guard runs at dial time:
// the server is real and reachable, the request never gets to it, and a
// hostname that resolves to loopback is refused like the literal address.
func TestNewTransport_RefusesLoopbackServer(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(u.Host)
	require.NoError(t, err)

	client := &http.Client{Transport: egress.NewTransport(nil), Timeout: 5 * time.Second}
	for _, target := range []string{srv.URL, "http://localhost:" + port + "/"} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		require.Error(t, err, target)
		assert.True(t, errors.Is(err, egress.ErrBlockedAddress), "%s: %v", target, err)
	}
	assert.Zero(t, hits.Load(), "the loopback server must not receive a request")

	// Control check: the same server answers a client without the guard.
	resp, err := srv.Client().Get(srv.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, int32(1), hits.Load())
}

// TestNewTransport_ProxyAddressIsChecked: with a proxy, the connection goes
// to the proxy, so the dial-time check applies to the proxy's address. A
// loopback proxy is refused.
func TestNewTransport_ProxyAddressIsChecked(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)

	client := &http.Client{Transport: egress.NewTransport(http.ProxyURL(proxyURL)), Timeout: 5 * time.Second}
	resp, err := client.Get("http://10.0.0.1:9090/")
	if resp != nil {
		_ = resp.Body.Close()
	}
	assert.ErrorIs(t, err, egress.ErrBlockedAddress)
}

// TestNewTransport_ProxyChecksTarget calls the transport's Proxy function
// directly: when a proxy is chosen, a denied target is refused before the
// request is handed to the proxy, whether it is an IP literal or a name.
func TestNewTransport_ProxyChecksTarget(t *testing.T) {
	proxyURL := &url.URL{Scheme: "http", Host: "10.0.0.2:3128"}
	tr := egress.NewTransport(http.ProxyURL(proxyURL))

	tests := []struct {
		target  string
		blocked bool
	}{
		{"http://169.254.169.254/latest/meta-data/", true},
		{"http://[fd00:ec2::254]/latest/meta-data/", true},
		{"http://100.100.100.200/latest/meta-data/", true},
		{"https://168.63.129.16/", true},
		{"http://[fe80::1%25eth0]/", true},
		{"http://[::ffff:127.0.0.1]:8082/api/v1/ui/pipelines", true},
		// A name is resolved and every address is checked.
		{"http://localhost:8082/api/v1/ui/pipelines", true},
		// A name that does not resolve cannot be checked (RFC 6761 .invalid).
		{"http://kardinal-egress-test.invalid/", true},
		{"http://10.0.0.5:9090/api/v1/query", false},
		{"https://8.8.8.8/", false},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, tt.target, nil)
			require.NoError(t, err)
			got, err := tr.Proxy(req)
			if tt.blocked {
				assert.ErrorIs(t, err, egress.ErrBlockedAddress)
				assert.Nil(t, got)
			} else {
				require.NoError(t, err)
				assert.Equal(t, proxyURL, got)
			}
		})
	}

	// When the Proxy function picks no proxy (NO_PROXY), the request goes
	// direct and the dial-time check applies instead.
	direct := egress.NewTransport(func(*http.Request) (*url.URL, error) { return nil, nil })
	req := httptest.NewRequest(http.MethodGet, "http://169.254.169.254/", nil)
	got, err := direct.Proxy(req)
	assert.NoError(t, err)
	assert.Nil(t, got)
}

// TestNewTransport_ProxyRefusesDeniedTargets runs a real forward proxy on a
// non-loopback address, so the dial-time check lets the connection to it
// through. Requests for metadata addresses never reach it, an allowed
// target does, and a redirect from the proxy to loopback is refused.
func TestNewTransport_ProxyRefusesDeniedTargets(t *testing.T) {
	var hits atomic.Int32
	proxy := newServerOnHostIP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://127.0.0.1:8082/api/v1/ui/pipelines", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	client := &http.Client{Transport: egress.NewTransport(http.ProxyURL(proxyURL)), Timeout: 10 * time.Second}

	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/",
		"http://[fd00:ec2::254]/latest/meta-data/",
		"http://100.100.100.200/latest/meta-data/",
		"http://localhost:8082/api/v1/ui/pipelines",
	} {
		resp, err := client.Get(target)
		if resp != nil {
			_ = resp.Body.Close()
		}
		assert.ErrorIs(t, err, egress.ErrBlockedAddress, target)
	}
	assert.Zero(t, hits.Load(), "a denied target must not reach the proxy")

	resp, err := client.Get("http://10.0.0.5:9090/ok")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, int32(1), hits.Load(), "an allowed target goes through the proxy")

	resp, err = client.Get("http://10.0.0.5:9090/redirect")
	if resp != nil {
		_ = resp.Body.Close()
	}
	assert.ErrorIs(t, err, egress.ErrBlockedAddress)
	assert.Equal(t, int32(2), hits.Load(), "the redirect to loopback must not be sent to the proxy")
}

// TestNewTransport_RedirectToLoopbackRefused: without a proxy, a server on
// an allowed address that redirects to loopback does not get the request
// through; the redirect's connection is checked like the first one.
func TestNewTransport_RedirectToLoopbackRefused(t *testing.T) {
	var loopbackHits atomic.Int32
	loopback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		loopbackHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer loopback.Close()

	var hits atomic.Int32
	redirector := newServerOnHostIP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, loopback.URL+"/", http.StatusFound)
	}))

	client := &http.Client{Transport: egress.NewTransport(nil), Timeout: 10 * time.Second}
	resp, err := client.Get(redirector.URL + "/hook")
	if resp != nil {
		_ = resp.Body.Close()
	}
	assert.ErrorIs(t, err, egress.ErrBlockedAddress)
	assert.Equal(t, int32(1), hits.Load(), "the first server is allowed")
	assert.Zero(t, loopbackHits.Load(), "the redirect to loopback must be refused")
}

// newServerOnHostIP starts h on a non-loopback IPv4 address of this host,
// which the guard allows, and skips the test if the host has none.
func newServerOnHostIP(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	ip, ok := hostIPv4()
	if !ok {
		t.Skip("no non-loopback IPv4 address on this host")
	}
	ln, err := net.Listen("tcp4", net.JoinHostPort(ip.String(), "0"))
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(h)
	require.NoError(t, srv.Listener.Close())
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// hostIPv4 returns a non-loopback, non-link-local IPv4 address of this host.
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
		if ip = ip.Unmap(); ok && ip.Is4() && egress.CheckAddr(ip) == nil {
			return ip, true
		}
	}
	return netip.Addr{}, false
}
