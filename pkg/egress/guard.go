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

// Package egress guards the HTTP requests the controller sends to URLs that
// users write into custom resources (NotificationHook webhooks, MetricCheck
// Prometheus URLs, custom step webhooks).
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
// guard checks the proxy's address and the proxy must apply its own policy.
package egress

import (
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
// HTTP(S)_PROXY and NO_PROXY, or nil to always connect directly.
func NewTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
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
