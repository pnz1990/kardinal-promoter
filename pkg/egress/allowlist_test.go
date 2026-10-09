// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package egress_test

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
)

// setAllowlist sets the controller-wide allowlist for one test. Tests in this
// package do not run in parallel, so the global is safe to change.
func setAllowlist(t *testing.T, entries ...string) *egress.Allowlist {
	t.Helper()
	a, err := egress.ParseAllowlist(entries)
	require.NoError(t, err)
	egress.SetAllowlist(a)
	t.Cleanup(func() { egress.SetAllowlist(nil) })
	return a
}

func TestParseAllowlist(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		wantErr string
		want    string
	}{
		{name: "none", entries: nil},
		{name: "blank entries only", entries: []string{"", "  "}},
		{name: "names, wildcards, CIDRs and addresses",
			entries: []string{"Hooks.Slack.com.", "*.webhook.office.com", "10.0.0.0/8", "fd00::/8", "192.168.1.7", "[fd00::1]"},
			want:    "hooks.slack.com.,*.webhook.office.com,10.0.0.0/8,fd00::/8,192.168.1.7,[fd00::1]"},
		{name: "bad CIDR", entries: []string{"10.0.0.0/33"}, wantErr: `"10.0.0.0/33": invalid CIDR`},
		{name: "inner wildcard", entries: []string{"hooks.*.com"}, wantErr: "only a leading *. wildcard"},
		{name: "bare wildcard", entries: []string{"*"}, wantErr: "invalid host name"},
		{name: "wildcard of nothing", entries: []string{"*."}, wantErr: "invalid host name"},
		{name: "URL instead of a host", entries: []string{"https://hooks.slack.com"}, wantErr: "invalid CIDR"},
		{name: "port", entries: []string{"hooks.slack.com:443"}, wantErr: "only a leading"},
		{name: "empty label", entries: []string{"hooks..slack.com"}, wantErr: "invalid host name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := egress.ParseAllowlist(tt.entries)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.want == "" {
				assert.Nil(t, a, "no entries is no allowlist")
				return
			}
			assert.Equal(t, tt.want, a.String())
		})
	}
}

func TestAllowlist_Matching(t *testing.T) {
	a, err := egress.ParseAllowlist([]string{"hooks.slack.com", "*.webhook.office.com", "10.1.0.0/16", "192.168.1.7", "fd00::/8"})
	require.NoError(t, err)

	names := map[string]bool{
		"hooks.slack.com":                true,
		"HOOKS.SLACK.COM.":               true,
		"evil-hooks.slack.com":           false,
		"slack.com":                      false,
		"x.hooks.slack.com":              false,
		"acme.webhook.office.com":        true,
		"a.b.webhook.office.com":         true,
		"webhook.office.com":             false,
		"acmewebhook.office.com":         false,
		"10.1.2.3":                       false, // addresses are matched with AllowsAddr only
		"":                               false,
		"webhook.office.com.attacker.io": false,
	}
	for host, want := range names {
		assert.Equal(t, want, a.AllowsName(host), "AllowsName(%q)", host)
	}
	addrs := map[string]bool{
		"10.1.2.3":        true,
		"10.2.0.1":        false,
		"192.168.1.7":     true,
		"192.168.1.8":     false,
		"::ffff:10.1.0.1": true, // IPv4-mapped is matched as IPv4
		"fd00::1":         true,
		"fe80::1":         false,
		"2001:db8::1":     false,
		"10.1.255.255":    true,
		"169.254.169.254": false,
		"fd00:ec2::254":   true, // in a CIDR entry, still denied by CheckAddr at dial time
		"100.100.100.200": false,
		"0.0.0.0":         false,
		"255.255.255.255": false,
		"10.0.255.255":    false,
	}
	for s, want := range addrs {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		assert.Equal(t, want, a.AllowsAddr(ip), "AllowsAddr(%s)", s)
	}

	var none *egress.Allowlist
	assert.True(t, none.AllowsName("anything"), "a nil allowlist allows everything")
	assert.True(t, none.AllowsAddr(netip.MustParseAddr("8.8.8.8")))
}

// TestNewTransport_AllowlistDirect: with an allowlist, a direct connection
// to an address in no CIDR entry is refused with ErrNotAllowlisted (which is
// also ErrBlockedAddress), one inside a CIDR entry goes through, and a
// transport created before SetAllowlist enforces it too.
func TestNewTransport_AllowlistDirect(t *testing.T) {
	var hits atomic.Int32
	srv := newServerOnHostIP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	host := strings.TrimPrefix(srv.URL, "http://")
	ip := netip.MustParseAddr(strings.Split(host, ":")[0])
	client := &http.Client{Transport: egress.NewTransport(nil), Timeout: 5 * time.Second}

	get := func() error {
		resp, err := client.Get(srv.URL + "/hook")
		if resp != nil {
			_ = resp.Body.Close()
		}
		client.CloseIdleConnections()
		return err
	}

	require.NoError(t, get(), "no allowlist: allowed")
	assert.Equal(t, int32(1), hits.Load())

	setAllowlist(t, "hooks.slack.com", "203.0.113.0/24")
	err := get()
	require.Error(t, err)
	assert.ErrorIs(t, err, egress.ErrNotAllowlisted)
	assert.ErrorIs(t, err, egress.ErrBlockedAddress)
	assert.Contains(t, err.Error(), "destination address is not allowed: not in the controller egress allowlist: "+ip.String()+" matches no entry")
	assert.Equal(t, int32(1), hits.Load(), "refused before connecting")

	setAllowlist(t, netip.PrefixFrom(ip, 32).String())
	require.NoError(t, get(), "the address is in a CIDR entry")
	assert.Equal(t, int32(2), hits.Load())
}

// TestNewTransport_AllowlistNeverAllowsDenied: an allowlist entry cannot
// open the deny list: loopback and metadata addresses stay refused with
// ErrBlockedAddress even when listed.
func TestNewTransport_AllowlistNeverAllowsDenied(t *testing.T) {
	setAllowlist(t, "127.0.0.0/8", "localhost", "169.254.0.0/16")
	client := &http.Client{Transport: egress.NewTransport(nil), Timeout: 5 * time.Second}
	for _, target := range []string{"http://127.0.0.1:8082/api/v1/ui/pipelines", "http://localhost:8082/", "http://169.254.1.1/"} {
		resp, err := client.Get(target)
		if resp != nil {
			_ = resp.Body.Close()
		}
		require.Error(t, err, target)
		assert.ErrorIs(t, err, egress.ErrBlockedAddress, target)
		assert.NotErrorIs(t, err, egress.ErrNotAllowlisted, "%s: refused by the deny list, not the allowlist", target)
	}
}

// TestNewTransport_AllowlistByName: a host entry matches the name in the
// URL; an address literal is not matched against host entries.
func TestNewTransport_AllowlistByName(t *testing.T) {
	var hits atomic.Int32
	srv := newServerOnHostIP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	srvIP := strings.Split(strings.TrimPrefix(srv.URL, "http://"), ":")[0]

	client := &http.Client{Transport: egress.NewTransport(nil), Timeout: 5 * time.Second}

	setAllowlist(t, "kardinal-egress.test")
	// A name that is not listed: refused, before any connection.
	resp, err := client.Get("http://" + srvIP + ":" + port + "/")
	if resp != nil {
		_ = resp.Body.Close()
	}
	assert.ErrorIs(t, err, egress.ErrNotAllowlisted)
	assert.Zero(t, hits.Load())

	// The listed name: allowed by name. The test host cannot resolve it, so
	// the error is a lookup failure, not the allowlist.
	resp, err = client.Get("http://kardinal-egress.test:" + port + "/")
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err)
	assert.NotErrorIs(t, err, egress.ErrNotAllowlisted, "a listed name passes the allowlist")
}

// TestNewTransport_AllowlistProxy: through a proxy, the target is checked
// against the allowlist before the request goes to the proxy, while the
// connection to the proxy itself is exempt (the operator configured it).
func TestNewTransport_AllowlistProxy(t *testing.T) {
	var hits atomic.Int32
	proxy := newServerOnHostIP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	client := &http.Client{Transport: egress.NewTransport(http.ProxyURL(proxyURL)), Timeout: 10 * time.Second}

	setAllowlist(t, "10.0.0.0/24")
	resp, err := client.Get("http://10.9.0.5:9090/denied")
	if resp != nil {
		_ = resp.Body.Close()
	}
	assert.ErrorIs(t, err, egress.ErrNotAllowlisted)
	assert.Zero(t, hits.Load(), "a target outside the allowlist never reaches the proxy")

	resp, err = client.Get("http://10.0.0.5:9090/ok")
	require.NoError(t, err, "the proxy's own address is not in the allowlist and is still reachable")
	_ = resp.Body.Close()
	assert.Equal(t, int32(1), hits.Load())
}
