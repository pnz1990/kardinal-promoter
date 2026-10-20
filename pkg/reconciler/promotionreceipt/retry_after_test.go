// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionreceipt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
)

// TestReconcile_SignRetryAfter (#2062, the receipt burst): when Rekor answers
// 429 with Retry-After, the receipt waits the longer of that wait and its
// backoff (30s at the first attempt); a Retry-After over
// receipt.MaxRetryAfter counts as that; the jitter is added on top; the
// attempt's time is recorded; and no request is sent before nextRetryAt.
//
// Covers RECEIPT-RETRY-AFTER-01.
func TestReconcile_SignRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name, retryAfter string
		jitter           func(time.Duration) time.Duration
		want             time.Duration
	}{
		{"longer than the backoff", "45", noJitter, 45 * time.Second},
		{"shorter than the backoff: the backoff", "1", noJitter, retryBase},
		{"capped", "3600", noJitter, receipt.MaxRetryAfter},
		{"with jitter", "40", func(d time.Duration) time.Duration { return d / 2 }, time.Minute},
		{"none: the backoff", "", noJitter, retryBase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				http.Error(w, "slow down", http.StatusTooManyRequests)
			}))
			defer srv.Close()
			secret, _ := keySecret(t)
			h := newHarness(t, secret, newReceipt(t, "ghcr.io/acme/checkout", "sha256:"+strings.Repeat("a", 64)))
			h.r.Signer.RekorURL, h.r.Signer.RekorVersion = srv.URL, 2
			h.r.JitterFn = tc.jitter
			rc := h.run(t, 1)
			assert.Equal(t, v1alpha1.ReceiptPending, rc.Status.Phase)
			assert.Equal(t, 1, rc.Status.Attempts)
			require.NotNil(t, rc.Status.NextRetryAt)
			assert.Equal(t, now.Add(tc.want), rc.Status.NextRetryAt.UTC())
			require.NotNil(t, rc.Status.LastAttemptAt)
			assert.Equal(t, now, rc.Status.LastAttemptAt.UTC())
			sent := calls.Load()
			require.Positive(t, sent)

			// Before nextRetryAt nothing is sent.
			h.r.NowFn = func() time.Time { return now.Add(tc.want - time.Second) }
			rc = h.run(t, 2)
			assert.Equal(t, 1, rc.Status.Attempts)
			assert.Equal(t, sent, calls.Load(), "no request before nextRetryAt")
			h.r.NowFn = func() time.Time { return now.Add(tc.want) }
			rc = h.run(t, 1)
			assert.Equal(t, 2, rc.Status.Attempts)
			assert.Greater(t, calls.Load(), sent)
		})
	}
}

// TestReconcile_SignRetryAfterGrows (QA-2 on #2192): an endpoint that
// always answers 503 with Retry-After: 1 does not make the receipt retry
// every second: the waits follow the backoff, doubling.
func TestReconcile_SignRetryAfterGrows(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	secret, _ := keySecret(t)
	h := newHarness(t, secret, newReceipt(t, "ghcr.io/acme/checkout", "sha256:"+strings.Repeat("a", 64)))
	h.r.Signer.RekorURL, h.r.Signer.RekorVersion = srv.URL, 2
	at := now
	var waits []time.Duration
	for range 5 {
		h.r.NowFn = func() time.Time { return at }
		rc := h.run(t, 1)
		require.NotNil(t, rc.Status.NextRetryAt)
		waits = append(waits, rc.Status.NextRetryAt.Sub(at))
		at = rc.Status.NextRetryAt.Time
	}
	assert.Equal(t, []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}, waits,
		"the waits grow, whatever the short Retry-After")
	assert.Positive(t, calls.Load())
}

// TestJitter: the default jitter is in [0, d], and not always the same.
func TestJitter(t *testing.T) {
	r := &Reconciler{}
	seen := map[time.Duration]bool{}
	for range 200 {
		j := r.jitter(10 * time.Second)
		require.GreaterOrEqual(t, j, time.Duration(0))
		require.LessOrEqual(t, j, 10*time.Second)
		seen[j] = true
	}
	assert.Greater(t, len(seen), 50, "random")
	assert.Zero(t, r.jitter(0))
}
