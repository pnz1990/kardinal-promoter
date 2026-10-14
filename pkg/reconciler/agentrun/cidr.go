// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentrun

import (
	"fmt"
	"net"
	"strings"
)

// parseCIDR returns s as a CIDR: s itself when it is one, a single address
// (/32 or /128) when it is an IP.
func parseCIDR(s string) (string, error) {
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return "", fmt.Errorf("not a CIDR: %w", err)
		}
		return n.String(), nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return "", fmt.Errorf("%q is not an IP address or CIDR", s)
	}
	if ip.To4() != nil {
		return ip.String() + "/32", nil
	}
	return ip.String() + "/128", nil
}
