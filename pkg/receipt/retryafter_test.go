// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"3", 3 * time.Second, true},
		{" 120 ", 2 * time.Minute, true},
		{"0", 0, true},
		{"999999999999", MaxRetryAfter, true},
		{now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
		{now.Add(time.Hour).Format(http.TimeFormat), MaxRetryAfter, true},
		{"-1", 0, false},
		{"soon", 0, false},
		{"", 0, false},
	} {
		got, ok := parseRetryAfter(tc.in, now)
		assert.Equal(t, tc.ok, ok, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}
}

// TestRetryAfterTransport: the longest Retry-After of 429 and 503 answers
// is recorded; other answers' headers are not.
func TestRetryAfterTransport(t *testing.T) {
	answers := []struct {
		code int
		ra   string
	}{{http.StatusTooManyRequests, "4"}, {http.StatusServiceUnavailable, "9"}, {http.StatusOK, "60"}, {http.StatusTooManyRequests, "2"}}
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		a := answers[i]
		i++
		w.Header().Set("Retry-After", a.ra)
		w.WriteHeader(a.code)
	}))
	defer srv.Close()
	rt := newRetryAfterTransport(nil)
	c := &http.Client{Transport: rt}
	for range answers {
		resp, err := c.Get(srv.URL)
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
	assert.Equal(t, 9*time.Second, rt.longest(), "the 200's header does not count")
	assert.Zero(t, newRetryAfterTransport(nil).longest())
}
