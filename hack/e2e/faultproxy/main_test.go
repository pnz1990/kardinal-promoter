// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFaultProxy: a route proxies to its target with the target's Host;
// down closes the connection, status answers the code, slow delays; the
// admin API sets the mode and reports the counts by status.
func TestFaultProxy(t *testing.T) {
	var host string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host = r.Host
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()
	r, _, err := newRoute("fulcio=:0=" + upstream.URL)
	require.NoError(t, err)
	proxy := httptest.NewServer(r)
	defer proxy.Close()
	admin := httptest.NewServer(routes{r}.admin())
	defer admin.Close()
	set := func(q string) {
		resp, err := http.Post(admin.URL+"/set?route=fulcio&"+q, "", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusNoContent, resp.StatusCode, q)
	}
	get := func() (*http.Response, error) {
		c := &http.Client{Timeout: 5 * time.Second}
		return c.Post(proxy.URL+"/api/v2/signingCert", "application/json", strings.NewReader("{}"))
	}

	resp, err := get()
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, strings.TrimPrefix(upstream.URL, "http://"), host, "the target's Host")

	set("mode=down")
	_, err = get()
	assert.Error(t, err, "down closes the connection")
	set("mode=status&code=429")
	resp, err = get()
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	set("mode=slow&delay=300ms")
	start := time.Now()
	resp, err = get()
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond)

	ra, err := http.Get(admin.URL + "/routes")
	require.NoError(t, err)
	body, _ := io.ReadAll(ra.Body)
	var got []struct {
		Name   string         `json:"name"`
		Mode   string         `json:"mode"`
		Counts map[string]int `json:"counts"`
	}
	require.NoError(t, json.Unmarshal(body, &got))
	require.Len(t, got, 1)
	assert.Equal(t, "slow", got[0].Mode)
	assert.Equal(t, map[string]int{"201": 2, "down": 1, "429": 1}, got[0].Counts)

	for _, bad := range []string{"mode=slow", "mode=status&code=200", "mode=nope"} {
		resp, err := http.Post(admin.URL+"/set?route=fulcio&"+bad, "", nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, bad)
	}
}

// TestFaultProxyRateLimit: ratelimit proxies at most rps requests in each
// second and answers the rest 429 with Retry-After; the next second admits
// rps again.
func TestFaultProxyRateLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	r, _, err := newRoute("fulcio=:0=" + upstream.URL)
	require.NoError(t, err)
	clock := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return clock }
	proxy := httptest.NewServer(r)
	defer proxy.Close()
	admin := httptest.NewServer(routes{r}.admin())
	defer admin.Close()
	for q, want := range map[string]int{"mode=ratelimit": http.StatusBadRequest, "mode=ratelimit&rps=0": http.StatusBadRequest,
		"mode=ratelimit&rps=3&ra=0": http.StatusBadRequest, "mode=ratelimit&rps=3&ra=3": http.StatusNoContent} {
		resp, err := http.Post(admin.URL+"/set?route=fulcio&"+q, "", nil)
		require.NoError(t, err)
		assert.Equal(t, want, resp.StatusCode, q)
	}
	burst := func(n int) (ok, limited int) {
		for range n {
			resp, err := http.Get(proxy.URL + "/api/v2/signingCert")
			require.NoError(t, err)
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK:
				ok++
			case http.StatusTooManyRequests:
				assert.Equal(t, "3", resp.Header.Get("Retry-After"))
				limited++
			}
		}
		return ok, limited
	}
	ok, limited := burst(10)
	assert.Equal(t, 3, ok)
	assert.Equal(t, 7, limited)
	clock = clock.Add(1100 * time.Millisecond)
	ok, limited = burst(5)
	assert.Equal(t, 3, ok, "the next second admits rps again")
	assert.Equal(t, 2, limited)
	assert.Equal(t, map[string]int{"200": 6, "429": 9}, r.Counts)
}

// TestFaultProxyStatusRetryAfter: status answers its code, with Retry-After
// when ra is set and none otherwise.
func TestFaultProxyStatusRetryAfter(t *testing.T) {
	r, _, err := newRoute("fulcio=:0=http://127.0.0.1:1")
	require.NoError(t, err)
	proxy := httptest.NewServer(r)
	defer proxy.Close()
	admin := httptest.NewServer(routes{r}.admin())
	defer admin.Close()
	for _, tc := range []struct{ q, want string }{{"mode=status&code=503&ra=1", "1"}, {"mode=status&code=503", ""}} {
		resp, err := http.Post(admin.URL+"/set?route=fulcio&"+tc.q, "", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
		resp, err = http.Get(proxy.URL + "/api/v2/signingCert")
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
		assert.Equal(t, tc.want, resp.Header.Get("Retry-After"), tc.q)
	}
	resp, err := http.Post(admin.URL+"/set?route=fulcio&mode=status&code=503&ra=0", "", nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
