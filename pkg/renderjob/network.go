// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package renderjob

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
)

// ErrNetworkRefused is every network request of the render process except
// git to the Pipeline's repository.
var ErrNetworkRefused = errors.New("network access is not allowed in the render Job")

// refuseTransport refuses every request.
type refuseTransport struct{}

func (refuseTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("%w (%s %s)", ErrNetworkRefused, r.Method, r.URL.Redacted())
}

// gitHostPort is the host:port of a git URL, with the scheme's default
// port.
func gitHostPort(raw string) (scheme, hostPort string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse spec.git.url: %w", err)
	}
	switch u.Scheme {
	case "https", "http":
	case "file":
		return u.Scheme, "", nil
	default:
		return "", "", fmt.Errorf("the render Job reaches git over http(s) only, not %q", u.Scheme)
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	return u.Scheme, net.JoinHostPort(strings.ToLower(u.Hostname()), port), nil
}

// gitTransport dials only hostPort, through the egress guard (no loopback,
// link-local or metadata addresses), and never through a proxy.
func gitTransport(hostPort string) *http.Transport {
	t := egress.NewTransport(nil)
	guarded := t.DialContext
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !strings.EqualFold(addr, hostPort) {
			return nil, fmt.Errorf("%w: git may reach %s only, not %s", ErrNetworkRefused, hostPort, addr)
		}
		return guarded(ctx, network, addr)
	}
	return t
}

// LockNetwork makes every network access of the render process impossible
// except git to the host of gitURL: net/http's default transport and client
// refuse every request (kustomize's remote loader, any library that fetches
// a URL), go-git gets an http client that dials only that host and port
// through the egress guard, and every other git transport is removed. A
// file:// URL (tests) keeps go-git's file transport and nothing else.
func LockNetwork(gitURL string) error {
	scheme, hostPort, err := gitHostPort(gitURL)
	if err != nil {
		return err
	}
	http.DefaultTransport = refuseTransport{}
	http.DefaultClient = &http.Client{Transport: refuseTransport{}}
	for s := range gitclient.Protocols {
		if s != scheme {
			gitclient.InstallProtocol(s, nil)
		}
	}
	if hostPort != "" {
		gitclient.InstallProtocol(scheme, githttp.NewClient(&http.Client{Transport: gitTransport(hostPort),
			// A redirect to another host would be refused by the dialer; a
			// redirect at all is not expected from a git server.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}))
	}
	return nil
}
