// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxRetryAfter caps how long a server's Retry-After is honoured: a
// receipt is tried again no later than this after the answer, whatever the
// server asked.
const MaxRetryAfter = 5 * time.Minute

// RetryAfterError is a signing error after which Fulcio, Rekor or the
// timestamp authority asked for a wait: an HTTP 429 or 503 with a
// Retry-After header. After is the longest such wait of the attempt,
// capped at MaxRetryAfter.
type RetryAfterError struct {
	After time.Duration
	Err   error
}

func (e *RetryAfterError) Error() string { return e.Err.Error() }

func (e *RetryAfterError) Unwrap() error { return e.Err }

// retryAfterTransport carries one Sign's requests and records the longest
// Retry-After of the 429 and 503 answers.
type retryAfterTransport struct {
	base http.RoundTripper
	now  func() time.Time

	mu    sync.Mutex
	after time.Duration
}

func newRetryAfterTransport(base http.RoundTripper) *retryAfterTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &retryAfterTransport{base: base, now: time.Now}
}

func (t *retryAfterTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable) {
		return resp, err
	}
	if d, ok := parseRetryAfter(resp.Header.Get("Retry-After"), t.now()); ok {
		t.mu.Lock()
		t.after = max(t.after, d)
		t.mu.Unlock()
	}
	return resp, nil
}

// longest is the longest Retry-After seen, 0 when none.
func (t *retryAfterTransport) longest() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.after
}

// parseRetryAfter reads a Retry-After value (RFC 9110 §10.2.3: delay
// seconds, or an HTTP date) as a wait from now, between 0 and
// MaxRetryAfter. ok is false when there is none or it does not parse.
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	var d time.Duration
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs < 0 {
			return 0, false
		}
		d = time.Duration(min(secs, int64(MaxRetryAfter/time.Second))) * time.Second
	} else if at, err := http.ParseTime(v); err == nil {
		d = at.Sub(now)
	} else {
		return 0, false
	}
	return min(max(d, 0), MaxRetryAfter), true
}
