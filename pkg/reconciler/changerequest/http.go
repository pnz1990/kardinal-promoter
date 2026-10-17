// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changerequest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

const (
	// requestTimeout bounds one provider request: a blackhole endpoint
	// holds a Limiter slot no longer than this.
	requestTimeout = 15 * time.Second
	// maxResponseBytes bounds a provider response.
	maxResponseBytes = 1 << 20
	// maxRetryAfter caps a provider's Retry-After.
	maxRetryAfter = time.Hour
)

// ProviderOptions configure the providers' HTTP clients.
type ProviderOptions struct {
	// RootCAs, when set, are the CAs a provider's TLS certificate may chain
	// to (--change-request-ca-file plus the system roots). Nil: the system
	// roots.
	RootCAs *x509.CertPool
}

// newHTTPClient is an egress-guarded client with a request timeout (call
// follows no redirect).
func newHTTPClient(o ProviderOptions) *http.Client {
	t := egress.NewTransport(http.ProxyFromEnvironment)
	t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: o.RootCAs}
	return &http.Client{Timeout: requestTimeout, Transport: t}
}

// LoadRootCAs is the system roots plus the PEM certificates of file.
func LoadRootCAs(pemData []byte) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, errors.New("no PEM certificate in the CA file")
	}
	return pool, nil
}

// CorrelationKey is the form of a correlation id a ticket carries:
// "kardinal-" and the first 32 hex characters of its SHA-256. A ServiceNow
// correlation_id holds 100 characters and a Jira label no spaces, while a
// correlation id can be longer than 300.
func CorrelationKey(correlationID string) string {
	sum := sha256.Sum256([]byte(correlationID))
	return "kardinal-" + hex.EncodeToString(sum[:])[:32]
}

// authHeader is the Authorization header of auth, from its Secret.
func authHeader(ctx context.Context, req Request, auth kardinalv1alpha1.ChangeRequestAuth) (string, error) {
	if req.Secret == nil {
		return "", errors.New("no credential reader")
	}
	v, err := req.Secret(ctx, auth.CredentialsSecretRef)
	if err != nil {
		return "", fmt.Errorf("credentials: %w", err)
	}
	if auth.Scheme == "bearer" {
		return "Bearer " + v, nil
	}
	if !strings.Contains(v, ":") {
		return "", fmt.Errorf("credentials: secret %q key %q must hold user:password for scheme basic",
			auth.CredentialsSecretRef.Name, auth.CredentialsSecretRef.Key)
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(v)), nil
}

// call sends method to base+path with query and body (JSON when not nil),
// and decodes a 2xx JSON answer into out. what names the call in errors,
// which never carry the URL, a credential or the body: 401 and 403 are
// ErrCredentials, 404 ErrNotFound, 429 and 5xx a *RetryError.
func call(ctx context.Context, hc *http.Client, auth, method string, base *url.URL, path string, query url.Values,
	body interface{}, out interface{}, what string) error {
	u := base.JoinPath(path)
	u.RawQuery = query.Encode()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s: encode: %w", what, err)
		}
		rd = bytes.NewReader(raw)
	}
	hreq, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return fmt.Errorf("%s: request: %w", what, scm.RedactErr(err))
	}
	hreq.Header.Set("Accept", "application/json")
	if body != nil {
		hreq.Header.Set("Content-Type", "application/json")
	}
	hreq.Header.Set("Authorization", auth)
	// Whatever client is given, a redirect is never followed: it would send
	// the credential to wherever the provider points.
	noRedirect := *hc
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(hreq)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			if uerr.Timeout() {
				return &RetryError{Err: fmt.Errorf("%s: timed out", what)}
			}
			if errors.Is(uerr.Err, egress.ErrBlockedAddress) {
				return fmt.Errorf("%s: %w", what, uerr.Err)
			}
			return &RetryError{Err: fmt.Errorf("%s: %w", what, scm.RedactErr(uerr.Err))}
		}
		return &RetryError{Err: fmt.Errorf("%s: %w", what, scm.RedactErr(err))}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return &RetryError{Status: resp.StatusCode, Err: fmt.Errorf("%s: read response: %w", what, err)}
	}
	switch s := resp.StatusCode; {
	case s == http.StatusUnauthorized || s == http.StatusForbidden:
		return fmt.Errorf("%s: HTTP %d: %w", what, s, ErrCredentials)
	case s == http.StatusNotFound:
		return fmt.Errorf("%s: HTTP 404: %w", what, ErrNotFound)
	case s == http.StatusTooManyRequests || s >= 500:
		return &RetryError{Status: s, After: retryAfter(resp.Header.Get("Retry-After"), time.Now()),
			Err: fmt.Errorf("%s: HTTP %d", what, s)}
	case s >= 300:
		return fmt.Errorf("%s: HTTP %d", what, s)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("%s: response larger than %d bytes", what, maxResponseBytes)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: the response is not the JSON expected", what)
	}
	return nil
}

// retryAfter parses a Retry-After header (seconds, or an HTTP date),
// capped at maxRetryAfter; 0 when absent or invalid.
func retryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil {
		return min(max(time.Duration(s)*time.Second, 0), maxRetryAfter)
	}
	if t, err := http.ParseTime(v); err == nil {
		return min(max(t.Sub(now), 0), maxRetryAfter)
	}
	return 0
}
