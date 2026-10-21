// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRegistryTransport: a test's own transport, not http.DefaultTransport,
// that keeps as many connections per host idle as it opens.
func TestRegistryTransport(t *testing.T) {
	def := http.DefaultTransport.(*http.Transport)
	for _, parallel := range []int{1, 4, 16, 200} {
		tr := RegistryTransport(t, parallel)
		assert.NotSame(t, def, tr, "a clone, not the shared transport")
		assert.Equal(t, parallel, tr.MaxIdleConnsPerHost, "parallel %d: idle per host", parallel)
		assert.Equal(t, parallel, tr.MaxConnsPerHost, "parallel %d: open per host", parallel)
		assert.GreaterOrEqual(t, tr.MaxIdleConns, parallel, "parallel %d: idle in all", parallel)
		assert.Equal(t, def.Proxy == nil, tr.Proxy == nil, "the default's proxy setting")
		assert.Equal(t, def.ForceAttemptHTTP2, tr.ForceAttemptHTTP2)
	}
	assert.Equal(t, 0, def.MaxConnsPerHost, "http.DefaultTransport is left as it was")
}
