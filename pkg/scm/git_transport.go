// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"io"
	"net/http"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// go-git's HTTP(S) transport, with the bytes of every smart-HTTP exchange
// counted in kardinal_git_transfer_bytes_total (#1529). The counted client
// wraps gitIdleTransport: http.DefaultTransport's settings (connection
// pooling, the environment's proxy) with the idle bound of git_dial.go on
// every connection.
//
// go-git configures a per-endpoint *http.Transport for a CA bundle, a client
// certificate, InsecureSkipTLS or a proxy in the clone or push options, and
// for that it needs the client's transport to be an *http.Transport: with the
// counting wrapper it would fail (or panic in its transport cache). Those
// endpoints go to go-git's default client unchanged, uncounted.
func init() {
	t := gitHTTPTransport{
		counted: githttp.NewClient(&http.Client{Transport: &countingTransport{base: gitIdleTransport}}),
		plain:   githttp.DefaultClient,
	}
	client.InstallProtocol("https", t)
	client.InstallProtocol("http", t)
}

// gitHTTPTransport picks the counted client, or go-git's default one for an
// endpoint with its own TLS or proxy settings.
type gitHTTPTransport struct {
	counted, plain transport.Transport
}

// ownTransport reports whether go-git builds a per-endpoint *http.Transport
// for ep (plumbing/transport/http newSession).
func ownTransport(ep *transport.Endpoint) bool {
	return len(ep.ClientKey) > 0 || len(ep.ClientCert) > 0 || len(ep.CaBundle) > 0 ||
		ep.InsecureSkipTLS || ep.Proxy.URL != ""
}

func (g gitHTTPTransport) pick(ep *transport.Endpoint) transport.Transport {
	if ownTransport(ep) {
		return g.plain
	}
	return g.counted
}

func (g gitHTTPTransport) NewUploadPackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.UploadPackSession, error) {
	return g.pick(ep).NewUploadPackSession(ep, auth)
}

func (g gitHTTPTransport) NewReceivePackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.ReceivePackSession, error) {
	return g.pick(ep).NewReceivePackSession(ep, auth)
}

// countingTransport counts request and response body bytes by git service.
type countingTransport struct {
	base http.RoundTripper
}

// gitService is the smart-HTTP service of a request: "fetch" for
// git-upload-pack (clone, fetch), "push" for git-receive-pack, "" otherwise.
func gitService(r *http.Request) string {
	s := r.URL.Path
	if q := r.URL.Query().Get("service"); q != "" {
		s = q
	}
	switch {
	case strings.HasSuffix(s, "git-upload-pack"):
		return "fetch"
	case strings.HasSuffix(s, "git-receive-pack"):
		return "push"
	}
	return ""
}

func (t *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	svc := gitService(r)
	if svc == "" {
		return t.base.RoundTrip(r)
	}
	if r.Body != nil && r.Body != http.NoBody {
		r = r.Clone(r.Context())
		r.Body = &countingBody{ReadCloser: r.Body, counter: GitTransferBytesTotal.WithLabelValues(svc, "sent")}
	}
	resp, err := t.base.RoundTrip(r)
	if err == nil && resp.Body != nil {
		resp.Body = &countingBody{ReadCloser: resp.Body, counter: GitTransferBytesTotal.WithLabelValues(svc, "received")}
	}
	return resp, err
}

// countingBody adds the bytes read through it to counter.
type countingBody struct {
	io.ReadCloser
	counter interface{ Add(float64) }
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.counter.Add(float64(n))
	}
	return n, err
}
