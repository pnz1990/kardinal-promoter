// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package source_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

// TestWatchers_Traced: every HTTP watcher the controller builds (OCI
// registry, git over HTTP, Helm HTTP index and OCI) records an OpenTelemetry
// client span per request, layered over the egress guard: the span names the
// host, and the loopback request is still refused before it reaches the
// server.
func TestWatchers_Traced(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev); _ = tp.Shutdown(context.Background()) })

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	hostPort := strings.TrimPrefix(srv.URL, "http://")

	tests := []struct {
		name  string
		watch func() error
	}{
		{name: "oci", watch: func() error {
			_, err := source.NewOCIWatcher(srv.URL+"/myorg/myapp", "").Watch(context.Background(), "")
			return err
		}},
		{name: "git http", watch: func() error {
			_, err := source.NewGitWatcher(srv.URL+"/repo.git", "main", "").Watch(context.Background(), "")
			return err
		}},
		{name: "helm http", watch: func() error {
			_, err := source.NewHelmWatcher(srv.URL, "podinfo").Watch(context.Background(), "")
			return err
		}},
		{name: "helm oci", watch: func() error {
			_, err := source.NewHelmWatcher("oci+http://"+hostPort, "podinfo").Watch(context.Background(), "")
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec.Reset()
			err := tc.watch()
			require.ErrorIs(t, err, egress.ErrBlockedAddress, "the egress guard still applies")
			var hosts []string
			for _, s := range rec.Ended() {
				if s.SpanKind() != trace.SpanKindClient || !strings.HasPrefix(s.Name(), "HTTP ") {
					continue
				}
				for _, a := range s.Attributes() {
					if a.Key == "server.address" {
						hosts = append(hosts, a.Value.AsString())
					}
				}
			}
			assert.Contains(t, hosts, "127.0.0.1", "a client span per request (spans: %d)", len(rec.Ended()))
		})
	}
	assert.Zero(t, hits.Load(), "no request reached the loopback server")
}
