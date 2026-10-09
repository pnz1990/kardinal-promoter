// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

// TestRunThenFlush_ExportsSpansEndedDuringShutdown: a span that ends while
// the manager drains (here: just before run returns, long before the batch
// timeout) is exported by the flush that follows, and run's error is kept.
func TestRunThenFlush_ExportsSpansEndedDuringShutdown(t *testing.T) {
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTracerProvider(prevTP); otel.SetTextMapPropagator(prevProp) })
	var mu sync.Mutex
	var got strings.Builder
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got.Write(b)
		mu.Unlock()
	}))
	defer ts.Close()
	shutdown, err := tracing.Setup(context.Background(), tracing.Config{Enabled: true, Endpoint: ts.URL, SamplingRatio: 1})
	require.NoError(t, err)

	stopped := errors.New("manager stopped")
	start := time.Now()
	err = runThenFlush(func() error {
		_, span := tracing.Start(context.Background(), "drained-reconcile")
		span.End()
		return stopped
	}, shutdown, zerolog.Nop())
	assert.ErrorIs(t, err, stopped)
	assert.Less(t, time.Since(start), 4*time.Second, "flushed at once, not after the 5s batch timeout")
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, got.String(), "drained-reconcile")
}
