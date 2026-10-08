// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package egress

import (
	"fmt"
	"net/netip"
	"strings"
	"sync/atomic"
)

// ErrNotAllowlisted is returned (wrapped) when an allowlist is set and a
// destination matches none of its entries. It wraps ErrBlockedAddress, so
// errors.Is(err, ErrBlockedAddress) holds for it too.
var ErrNotAllowlisted = fmt.Errorf("%w: not in the controller egress allowlist", ErrBlockedAddress)

// Allowlist narrows the destinations the guarded transports may reach. When
// one is set (SetAllowlist), a connection is allowed only when the request's
// host name matches a host entry, or every address the connection goes to is
// inside a CIDR entry. The deny list of CheckAddr still applies on top: an
// allowlist never permits loopback, link-local, metadata, unspecified or
// multicast addresses.
//
// Entries:
//
//	hooks.slack.com        exactly this host name
//	*.example.com          any name under example.com (not example.com itself)
//	10.0.0.0/8, fd00::/8   addresses in the prefix
//	10.1.2.3               this address only
type Allowlist struct {
	names    map[string]bool
	suffixes []string
	prefixes []netip.Prefix
	entries  []string
}

// ParseAllowlist parses allowlist entries. Empty entries are skipped; no
// entries at all returns nil, which means no allowlist (every destination
// that passes CheckAddr is allowed).
func ParseAllowlist(entries []string) (*Allowlist, error) {
	a := &Allowlist{names: map[string]bool{}}
	for _, raw := range entries {
		e := strings.ToLower(strings.TrimSpace(raw))
		if e == "" {
			continue
		}
		switch {
		case strings.Contains(e, "/"):
			p, err := netip.ParsePrefix(e)
			if err != nil {
				return nil, fmt.Errorf("egress allowlist entry %q: invalid CIDR: %w", raw, err)
			}
			a.prefixes = append(a.prefixes, p.Masked())
		case isIP(e):
			ip, _ := netip.ParseAddr(strings.Trim(e, "[]"))
			ip = ip.Unmap().WithZone("")
			a.prefixes = append(a.prefixes, netip.PrefixFrom(ip, ip.BitLen()))
		case strings.HasPrefix(e, "*."):
			suffix := strings.TrimSuffix(e[1:], ".") // ".example.com"
			if err := checkHostName(strings.TrimPrefix(suffix, ".")); err != nil {
				return nil, fmt.Errorf("egress allowlist entry %q: %w", raw, err)
			}
			a.suffixes = append(a.suffixes, suffix)
		default:
			name := strings.TrimSuffix(e, ".")
			if err := checkHostName(name); err != nil {
				return nil, fmt.Errorf("egress allowlist entry %q: %w", raw, err)
			}
			a.names[name] = true
		}
		a.entries = append(a.entries, e)
	}
	if len(a.entries) == 0 {
		return nil, nil
	}
	return a, nil
}

func isIP(s string) bool {
	_, err := netip.ParseAddr(strings.Trim(s, "[]"))
	return err == nil
}

// checkHostName accepts a DNS name: dot-separated labels of letters, digits,
// hyphens and underscores. A wildcard is only allowed as the first label, and
// ParseAllowlist strips it before calling this.
func checkHostName(name string) error {
	if name == "" || len(name) > 253 {
		return fmt.Errorf("invalid host name")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("invalid host name %q", name)
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return fmt.Errorf("invalid host name %q: only a leading *. wildcard is allowed", name)
			}
		}
	}
	return nil
}

// String lists the entries, comma-separated, as parsed.
func (a *Allowlist) String() string {
	if a == nil {
		return ""
	}
	return strings.Join(a.entries, ",")
}

// AllowsName reports whether host, a host name (not an address), matches a
// host entry. Matching ignores case and a trailing dot.
func (a *Allowlist) AllowsName(host string) bool {
	if a == nil {
		return true
	}
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if h == "" || isIP(h) {
		return false
	}
	if a.names[h] {
		return true
	}
	for _, s := range a.suffixes {
		if strings.HasSuffix(h, s) && len(h) > len(s) {
			return true
		}
	}
	return false
}

// AllowsAddr reports whether ip is inside a CIDR entry.
func (a *Allowlist) AllowsAddr(ip netip.Addr) bool {
	if a == nil {
		return true
	}
	ip = ip.Unmap().WithZone("")
	for _, p := range a.prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// current is the controller-wide allowlist; nil means none.
var current atomic.Pointer[Allowlist]

// SetAllowlist sets the controller-wide allowlist every transport from
// NewTransport enforces, including ones created before the call. nil removes
// it. The controller sets it once at startup (--egress-allowlist).
func SetAllowlist(a *Allowlist) { current.Store(a) }

// CurrentAllowlist returns the allowlist SetAllowlist set, or nil.
func CurrentAllowlist() *Allowlist { return current.Load() }

// checkAllowlisted returns an error wrapping ErrNotAllowlisted when an
// allowlist is set and ip, the address a connection to host goes to, is not
// allowed: host matches no host entry and ip is in no CIDR entry.
func checkAllowlisted(a *Allowlist, host string, ip netip.Addr) error {
	if a == nil || a.AllowsName(host) || a.AllowsAddr(ip) {
		return nil
	}
	if host == "" || isIP(host) {
		return fmt.Errorf("%w: %s matches no entry", ErrNotAllowlisted, ip)
	}
	return fmt.Errorf("%w: %s (%s) matches no entry", ErrNotAllowlisted, host, ip)
}
