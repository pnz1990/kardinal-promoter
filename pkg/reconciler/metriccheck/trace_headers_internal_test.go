// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordingTransport records the headers of every request it is given.
type recordingTransport struct{ headers []http.Header }

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.headers = append(r.headers, req.Header.Clone())
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Request: req}, nil
}

// TestMetricTransport_SendsNoTraceContext (QA #1545): inside a sampled trace,
// with baggage and the W3C propagators installed, a MetricCheck request
// reaches the transport below the tracing layer without traceparent,
// tracestate or baggage headers, while its client span is recorded.
func TestMetricTransport_SendsNoTraceContext(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
		_ = tp.Shutdown(context.Background())
	})

	member, err := baggage.NewMember("tenant", "acme")
	require.NoError(t, err)
	bag, err := baggage.New(member)
	require.NoError(t, err)
	ctx, span := tp.Tracer("test").Start(baggage.ContextWithBaggage(context.Background(), bag), "reconcile")
	defer span.End()

	base := &recordingTransport{}
	client := &http.Client{Transport: metricTransport(base)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.datadoghq.com/api/v1/query?query=x", nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	require.Len(t, base.headers, 1)
	for _, h := range []string{"Traceparent", "Tracestate", "Baggage"} {
		assert.Empty(t, base.headers[0].Get(h), "%s must not be sent to a metrics API", h)
	}
	var clientSpans int
	for _, s := range rec.Ended() {
		if s.Name() == "HTTP GET" && s.Parent().SpanID() == span.SpanContext().SpanID() {
			clientSpans++
		}
	}
	assert.Equal(t, 1, clientSpans, "the request is a client span under the reconcile")
}
