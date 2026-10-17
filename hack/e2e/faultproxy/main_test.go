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
