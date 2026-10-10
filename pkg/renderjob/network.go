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
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
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
// port. An ssh URL, ssh:// or scp-like (git@host:path), has the scheme ssh.
func gitHostPort(raw string) (scheme, hostPort string, err error) {
	if scm.IsSSHRemote(raw) {
		ep, err := transport.NewEndpoint(strings.TrimSpace(raw))
		if err != nil {
			return "", "", fmt.Errorf("parse spec.git.url: %w", err)
		}
		port := ep.Port
		if port == 0 {
			port = 22
		}
		return "ssh", net.JoinHostPort(strings.ToLower(ep.Host), strconv.Itoa(port)), nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse spec.git.url: %w", err)
	}
	switch u.Scheme {
	case "https", "http":
	case "file":
		return u.Scheme, "", nil
	default:
		return "", "", fmt.Errorf("the render Job reaches git over http(s) or ssh only, not %q", u.Scheme)
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	return u.Scheme, net.JoinHostPort(strings.ToLower(u.Hostname()), port), nil
}

// gitDial dials only hostPort, through the egress guard (no loopback,
// link-local or metadata addresses), and never through a proxy.
func gitDial(hostPort string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	guarded := egress.NewTransport(nil).DialContext
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !strings.EqualFold(addr, hostPort) {
			return nil, fmt.Errorf("%w: git may reach %s only, not %s", ErrNetworkRefused, hostPort, addr)
		}
		return guarded(ctx, network, addr)
	}
}

// gitTransport is the HTTP transport of git: it dials with gitDial.
func gitTransport(hostPort string) *http.Transport {
	t := egress.NewTransport(nil)
	t.DialContext = gitDial(hostPort)
	return t
}

// LockNetwork makes every network access of the render process impossible
// except git to the host of gitURL: net/http's default transport and client
// refuse every request (kustomize's remote loader, any library that fetches
// a URL), go-git gets an http client (for an ssh URL, kardinal's ssh dial)
// that dials only that host and port through the egress guard, and every
// other git transport is removed. A file:// URL (tests) keeps go-git's file
// transport and nothing else.
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
	if scheme == "ssh" {
		// kardinal's git client dials every ssh connection through scm's
		// dial (SetSSHDial), which reaches only the git host; host keys are
		// checked against knownHosts. The installed ssh transport (scm's,
		// which waits for receive-pack and bounds the dial) is wrapped so
		// that an ssh session without that dial (no ProxyOptions, which
		// go-git would dial directly) is refused instead.
		scm.SetSSHDial(gitDial(hostPort))
		if _, wrapped := gitclient.Protocols["ssh"].(scopedSSH); !wrapped {
			gitclient.InstallProtocol("ssh", scopedSSH{gitclient.Protocols["ssh"]})
		}
		return nil
	}
	if hostPort != "" {
		gitclient.InstallProtocol(scheme, githttp.NewClient(&http.Client{Transport: gitTransport(hostPort),
			// A redirect to another host would be refused by the dialer; a
			// redirect at all is not expected from a git server.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}))
	}
	return nil
}

// scopedSSH is an ssh transport that opens only sessions whose endpoint
// dials through kardinal's dial (its Proxy is scm.SSHDialScheme).
type scopedSSH struct{ transport.Transport }

func (s scopedSSH) check(ep *transport.Endpoint) error {
	if u, err := url.Parse(ep.Proxy.URL); err != nil || u.Scheme != scm.SSHDialScheme {
		return fmt.Errorf("%w: ssh to %s without kardinal's git dial", ErrNetworkRefused, ep.Host)
	}
	return nil
}

func (s scopedSSH) NewUploadPackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.UploadPackSession, error) {
	if err := s.check(ep); err != nil {
		return nil, err
	}
	return s.Transport.NewUploadPackSession(ep, auth)
}

func (s scopedSSH) NewReceivePackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.ReceivePackSession, error) {
	if err := s.check(ep); err != nil {
		return nil, err
	}
	return s.Transport.NewReceivePackSession(ep, auth)
}
