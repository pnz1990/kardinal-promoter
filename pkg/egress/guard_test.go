// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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

// TestNewTransport_ProxyAddressIsChecked documents the proxy caveat: with a
// proxy, the connection goes to the proxy, so the guard checks the proxy's
// address. A loopback proxy is refused.
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
