// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package tracing sets up OpenTelemetry tracing for the controller and
// provides the spans it records: one per reconcile, per promotion step, per
// git operation, and per outbound HTTP request (SCM APIs, NotificationHook
// webhooks), plus server spans for inbound SCM webhooks and Bundle API calls.
//
// Tracing is off unless Setup is called with Enabled. While it is off the
// global OpenTelemetry provider is the no-op one, so every helper here costs a
// few nanoseconds and records nothing.
//
// Spans never carry URLs, paths, query strings or headers: incoming-webhook
// URLs and git remotes can embed tokens. A request span records the method,
// the host and the status code only.
package tracing

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope of every kardinal span.
const ScopeName = "github.com/kardinal-promoter/kardinal-promoter"

// ServiceName is the service.name resource attribute of the controller.
const ServiceName = "kardinal-controller"

// Config configures the trace exporter.
type Config struct {
	// Enabled turns tracing on. When false Setup installs nothing.
	Enabled bool
	// Endpoint is the OTLP/HTTP endpoint: a URL such as
	// http://otel-collector.observability:4318 (the /v1/traces path is
	// added), or host:port. Empty uses OTEL_EXPORTER_OTLP_TRACES_ENDPOINT or
	// OTEL_EXPORTER_OTLP_ENDPOINT, else localhost:4318.
	Endpoint string
	// Insecure sends plain HTTP to a host:port Endpoint. A URL Endpoint's
	// scheme decides by itself.
	Insecure bool
	// SamplingRatio is the fraction of new traces sampled, 0 to 1. A trace
	// started by an inbound request carrying a sampled traceparent is always
	// recorded (parent-based).
	SamplingRatio float64
	// ServiceVersion is the service.version resource attribute.
	ServiceVersion string
	// OnError receives export and SDK errors (a collector that refuses a
	// batch, for example). Nil drops them.
	OnError func(error)
}

// Validate checks the configuration of an enabled exporter.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.SamplingRatio < 0 || c.SamplingRatio > 1 {
		return fmt.Errorf("tracing sampling ratio %v is not between 0 and 1", c.SamplingRatio)
	}
	if c.Endpoint != "" && strings.Contains(c.Endpoint, "://") {
		u, err := url.Parse(c.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("tracing endpoint %q is not an http:// or https:// URL or host:port", c.Endpoint)
		}
	}
	return nil
}

// exporterOptions maps the Config to otlptracehttp options.
func (c Config) exporterOptions() []otlptracehttp.Option {
	var opts []otlptracehttp.Option
	switch {
	case c.Endpoint == "":
		if c.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
	case strings.Contains(c.Endpoint, "://"):
		u, _ := url.Parse(c.Endpoint)
		path := strings.TrimRight(u.Path, "/")
		if !strings.HasSuffix(path, "/v1/traces") {
			path += "/v1/traces"
		}
		u.Path = path
		opts = append(opts, otlptracehttp.WithEndpointURL(u.String()))
	default:
		opts = append(opts, otlptracehttp.WithEndpoint(c.Endpoint))
		if c.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
	}
	return opts
}

// Setup installs the global tracer provider and the W3C trace context and
// baggage propagators. The returned shutdown flushes buffered spans; call it
// before the process exits. With Enabled false it installs nothing and
// shutdown does nothing.
func Setup(ctx context.Context, c Config) (shutdown func(context.Context) error, err error) {
	noop := func(context.Context) error { return nil }
	if !c.Enabled {
		return noop, nil
	}
	if err := c.Validate(); err != nil {
		return noop, err
	}
	exp, err := otlptracehttp.New(ctx, c.exporterOptions()...)
	if err != nil {
		return noop, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", ServiceName),
		attribute.String("service.version", c.ServiceVersion),
	))
	if err != nil {
		return noop, fmt.Errorf("tracing resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(c.SamplingRatio))),
	)
	if c.OnError != nil {
		otel.SetErrorHandler(otel.ErrorHandlerFunc(c.OnError))
	}
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return tp.Shutdown, nil
}

// Tracer is the tracer every kardinal span comes from.
func Tracer() trace.Tracer { return otel.Tracer(ScopeName) }

// Start starts an internal span. End it with End.
func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attrs...))
}

// End records err on span (when not nil) and ends it.
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// errFailed marks a span failed without an error value.
var errFailed = errors.New("failed")

// Fail marks span failed with message (a step that returned StepFailed).
func Fail(span trace.Span, message string) {
	if message == "" {
		message = errFailed.Error()
	}
	span.SetStatus(codes.Error, message)
}

// HostOf returns the host of a URL for span attributes, or "" when raw does
// not parse. The path and user info are never returned.
func HostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
