// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"io"
	"net/http"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// go-git's HTTP(S) transport, with the bytes of every smart-HTTP exchange
// counted in kardinal_git_transfer_bytes_total (#1529). It is the default
// transport (http.DefaultTransport, as go-git's own default client uses), so
// proxies, TLS and connection pooling are unchanged; only the bodies are
// counted as they are read.
func init() {
	c := githttp.NewClient(&http.Client{Transport: &countingTransport{base: http.DefaultTransport}})
	client.InstallProtocol("https", c)
	client.InstallProtocol("http", c)
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
