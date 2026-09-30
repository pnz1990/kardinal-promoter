// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package egress guards the HTTP requests the controller sends to URLs that
// users write into custom resources (NotificationHook webhooks and MetricCheck
// Prometheus URLs).
//
// The guard runs as a net.Dialer Control function, so it sees the address the
// connection is about to use after DNS resolution, on every connection,
// redirects included. A hostname that resolves (or re-resolves) to a denied
// address is refused. The guard denies:
//
//   - loopback (127.0.0.0/8, ::1): with no UI auth mode set the controller's
//     UI API trusts loopback peers, so a user URL must never reach it;
//   - link-local (169.254.0.0/16, fe80::/10), which holds the cloud metadata
//     and credential endpoints (169.254.169.254, 169.254.170.2, 169.254.170.23);
//   - other cloud metadata addresses outside link-local;
//   - unspecified (0.0.0.0/8, ::) and multicast addresses.
//
// Private ranges (10/8, 172.16/12, 192.168/16, fc00::/7) stay allowed: in-cluster
// Services and Prometheus are the normal targets. Use the chart NetworkPolicy
// (networkPolicy.enabled, networkPolicy.extraEgress) to narrow egress further.
//
// When the transport uses a proxy, the connection goes to the proxy, so the
// dial-time check sees only the proxy's address. The transport therefore also
// checks the target before it hands a request to a proxy: an IP address with
// CheckAddr, a host name by resolving it and checking every address. A name
// that does not resolve is refused. The proxy resolves the name again, so a
// name whose answers change between the two lookups can still get through;
// the proxy must apply its own policy too.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrBlockedAddress is returned (wrapped) when a connection would go to a
// denied address. errors.Is finds it through *url.Error and *net.OpError.
var ErrBlockedAddress = errors.New("destination address is not allowed")

// thisNetwork is 0.0.0.0/8. Linux treats a connect to 0.0.0.0 as a connect
// to the local host; the rest of the block is not routable.
var thisNetwork = netip.MustParsePrefix("0.0.0.0/8")

// metadataAddrs are cloud metadata and credential endpoints outside the
// link-local ranges, which are denied as a whole.
var metadataAddrs = map[netip.Addr]string{
	netip.MustParseAddr("fd00:ec2::254"):   "AWS instance metadata (IPv6)",
	netip.MustParseAddr("fd00:ec2::23"):    "EKS Pod Identity agent (IPv6)",
	netip.MustParseAddr("fd20:ce::254"):    "GCP metadata (IPv6)",
	netip.MustParseAddr("100.100.100.200"): "Alibaba Cloud metadata",
	netip.MustParseAddr("168.63.129.16"):   "Azure WireServer",
}

// CheckAddr returns an error wrapping ErrBlockedAddress when ip must not be
// dialed. IPv4-mapped IPv6 addresses are checked as IPv4.
func CheckAddr(ip netip.Addr) error {
	ip = ip.Unmap()
	var reason string
	switch {
	case !ip.IsValid():
		reason = "invalid address"
	case ip.IsLoopback():
		reason = "loopback"
	case ip.IsUnspecified(), thisNetwork.Contains(ip):
		reason = "unspecified"
	case ip.IsLinkLocalUnicast():
		reason = "link-local (cloud metadata)"
	case ip.IsMulticast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast():
		reason = "multicast"
	default:
		if name, ok := metadataAddrs[ip]; ok {
			reason = name
		}
	}
	if reason == "" {
		return nil
	}
	return fmt.Errorf("%w: %s is %s", ErrBlockedAddress, ip, reason)
}

// Control is a net.Dialer Control function that applies CheckAddr to the
// address of every connection. address is always a resolved IP and port.
func Control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrBlockedAddress, address)
	}
	// Drop an IPv6 zone: netip keeps it, and the check is about the address.
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrBlockedAddress, address)
	}
	return CheckAddr(ip.WithZone(""))
}

// NewTransport returns an http.Transport with the settings of
// http.DefaultTransport whose dialer applies Control to every connection.
// proxy is the Transport.Proxy function: http.ProxyFromEnvironment to honour
// HTTP(S)_PROXY and NO_PROXY, or nil to always connect directly. When proxy
// picks a proxy for a request, the request's target is checked first (see
// the package comment), on every request including redirects.
func NewTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	if proxy != nil {
		proxy = checkTargetBeforeProxy(proxy)
	}
	return &http.Transport{
		Proxy: proxy,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			Control:   Control,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// checkTargetBeforeProxy wraps a Transport.Proxy function. When it returns a
// proxy, the request's target host must pass CheckAddr: an IP literal
// directly, a host name through every address it resolves to.
func checkTargetBeforeProxy(proxy func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		proxyURL, err := proxy(req)
		if err != nil || proxyURL == nil {
			return proxyURL, err
		}
		if err := checkHost(req.Context(), req.URL.Hostname()); err != nil {
			return nil, err
		}
		return proxyURL, nil
	}
}

// checkHost applies CheckAddr to host, an IP literal or a name to resolve.
func checkHost(ctx context.Context, host string) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		return CheckAddr(ip.WithZone(""))
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err == nil && len(ips) == 0 {
		err = errors.New("no addresses")
	}
	if err != nil {
		return fmt.Errorf("%w: cannot resolve %q to check it before sending the request to the proxy: %v",
			ErrBlockedAddress, host, err)
	}
	for _, ip := range ips {
		if err := CheckAddr(ip.WithZone("")); err != nil {
			return fmt.Errorf("%w (%q resolves to it)", err, host)
		}
	}
	return nil
}
