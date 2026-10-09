// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package tracing

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// roundTripper records a client span for every request.
type roundTripper struct {
	base   http.RoundTripper
	inject bool
}

// Transport wraps base (http.DefaultTransport when nil) so every request
// gets a client span "HTTP <method>" with the method, host and status code.
// With inject, the request also carries the W3C traceparent (and
// tracestate, baggage) of that span: kardinal uses it for NotificationHook
// webhooks, so a receiver can join the trace. SCM API requests are traced
// without injection: trace headers are not sent to third-party APIs.
func Transport(base http.RoundTripper, inject bool) http.RoundTripper {
	return &roundTripper{base: base, inject: inject}
}

func (t *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	ctx, span := Tracer().Start(req.Context(), "HTTP "+req.Method,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("http.request.method", req.Method),
			attribute.String("server.address", req.URL.Hostname()),
			attribute.String("url.scheme", req.URL.Scheme),
		))
	defer span.End()
	if t.inject && span.SpanContext().IsValid() {
		req = req.Clone(ctx)
		otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	} else {
		req = req.WithContext(ctx)
	}
	resp, err := base.RoundTrip(req)
	if err != nil {
		// *url.Error repeats the full URL: record only the sanitized text.
		span.AddEvent("exception", trace.WithAttributes(attribute.String("exception.message", Sanitize(err.Error()))))
		span.SetStatus(codes.Error, "request failed")
		return resp, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	if resp.StatusCode >= 400 {
		span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
	return resp, nil
}

// CloseIdleConnections forwards to the base transport, so callers that
// close idle connections keep working through the wrapper.
func (t *roundTripper) CloseIdleConnections() {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if c, ok := base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// statusRecorder captures the status code a handler writes.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Handler wraps h with a server span named name. The endpoints it wraps are
// public (SCM webhooks, the Bundle API) and the trace context arrives before
// the caller is authenticated, so an inbound traceparent is not trusted as
// the parent: the span starts a new trace, sampled by the controller's own
// sampler, and links to the caller's span context (otelhttp's
// WithPublicEndpoint).
func Handler(name string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote := trace.SpanContextFromContext(
			otel.GetTextMapPropagator().Extract(context.Background(), propagation.HeaderCarrier(r.Header)))
		opts := []trace.SpanStartOption{
			trace.WithNewRoot(),
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", r.Method)),
		}
		if remote.IsValid() {
			opts = append(opts, trace.WithLinks(trace.Link{SpanContext: remote}))
		}
		ctx, span := Tracer().Start(r.Context(), name, opts...)
		defer span.End()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r.WithContext(ctx))
		span.SetAttributes(attribute.Int("http.response.status_code", rec.status))
		if rec.status >= 500 {
			span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", rec.status))
		}
	})
}

// tracedReconciler records a span per reconcile.
type tracedReconciler struct {
	controller string
	inner      reconcile.Reconciler
}

// WrapReconciler wraps r so every reconcile is a span "<controller>.Reconcile"
// with the object's namespace and name, the requeue delay, and the error.
func WrapReconciler(controller string, r reconcile.Reconciler) reconcile.Reconciler {
	return &tracedReconciler{controller: controller, inner: r}
}

func (t *tracedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := Tracer().Start(ctx, t.controller+".Reconcile", trace.WithAttributes(
		attribute.String("kardinal.controller", t.controller),
		attribute.String("k8s.namespace.name", req.Namespace),
		attribute.String("kardinal.object.name", req.Name),
	))
	res, err := t.inner.Reconcile(ctx, req)
	if res.RequeueAfter > 0 {
		span.SetAttributes(attribute.Int64("kardinal.requeue_after_ms", res.RequeueAfter.Milliseconds()))
	}
	End(span, err)
	return res, err
}
