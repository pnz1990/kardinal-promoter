// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"fmt"
	"net"
	"strings"
)

// loopbackUIHosts are always allowed: kubectl port-forward and a browser on
// the same machine reach the UI through them.
var loopbackUIHosts = map[string]struct{}{"localhost": {}, "127.0.0.1": {}, "::1": {}}

// uiHostNotAllowedMsg is the 403 body for an /api/ request whose Host is not
// on the allowlist while UI auth is off.
const uiHostNotAllowedMsg = "UI API: host not allowed; add it to --ui-allowed-hosts " +
	"(Helm value ui.allowedHosts)"

// uiHostAllowlist is the set of Host header names the UI server accepts as
// its own name, on top of loopbackUIHosts (--ui-allowed-hosts).
//
// DNS rebinding: a page on evil.example whose name is re-resolved to the
// controller's address is same-origin with the controller under that name.
// The browser then sends Origin and Host headers that match each other, so
// Origin == Host proves nothing. Only a Host the operator named is trusted.
// A nil allowlist allows loopback only.
type uiHostAllowlist map[string]struct{}

// parseUIAllowedHosts parses --ui-allowed-hosts: a comma-separated list of
// host names or IP addresses, without scheme, path or wildcard. A port is
// accepted and ignored.
func parseUIAllowedHosts(csv string) (uiHostAllowlist, error) {
	hosts := uiHostAllowlist{}
	for _, raw := range strings.Split(csv, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.ContainsAny(raw, "/*@ \t") {
			return nil, fmt.Errorf("--ui-allowed-hosts entry %q: want a host name without scheme, path or wildcard", raw)
		}
		h := normalizeUIHost(raw)
		if h == "" {
			return nil, fmt.Errorf("--ui-allowed-hosts entry %q: empty host name", raw)
		}
		hosts[h] = struct{}{}
	}
	return hosts, nil
}

// allows reports whether the request's Host header names this server.
func (a uiHostAllowlist) allows(hostport string) bool {
	h := normalizeUIHost(hostport)
	if h == "" {
		return false
	}
	if _, ok := loopbackUIHosts[h]; ok {
		return true
	}
	_, ok := a[h]
	return ok
}

// names returns the configured host names, for the startup log.
func (a uiHostAllowlist) names() []string {
	out := make([]string, 0, len(a))
	for h := range a {
		out = append(out, h)
	}
	return out
}

// normalizeUIHost drops the port, IPv6 brackets and a trailing dot from a
// Host header value and lower-cases it.
func normalizeUIHost(hostport string) string {
	h := strings.TrimSpace(hostport)
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	h = strings.TrimSuffix(h, ".")
	return strings.ToLower(h)
}
