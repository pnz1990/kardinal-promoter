// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package tracing_test

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/tracing"
)

// recordSpans installs an in-memory tracer provider for one test. Tests in
// this package do not run in parallel: the provider is global.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})
	return rec
}

func attrs(s sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	m := map[attribute.Key]attribute.Value{}
	for _, kv := range s.Attributes() {
		m[kv.Key] = kv.Value
	}
	return m
}

// headerServer records the traceparent header of every request.
type headerServer struct {
	mu     sync.Mutex
	parent []string
	status int
}

func (h *headerServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.parent = append(h.parent, r.Header.Get("traceparent"))
	h.mu.Unlock()
	if h.status != 0 {
		w.WriteHeader(h.status)
	}
}

func TestTransport_ClientSpanAndInjection(t *testing.T) {
	rec := recordSpans(t)
	srv := &headerServer{status: http.StatusServiceUnavailable}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	for _, inject := range []bool{true, false} {
		c := &http.Client{Transport: tracing.Transport(nil, inject)}
		ctx, parent := otel.Tracer("test").Start(context.Background(), "parent")
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/services/T0/B0/SECRET?token=SECRET", nil)
		require.NoError(t, err)
		resp, err := c.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		parent.End()
	}

	spans := rec.Ended()
	var clients []sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.SpanKind() == trace.SpanKindClient {
			clients = append(clients, s)
		}
	}
	require.Len(t, clients, 2)
	for _, s := range clients {
		assert.Equal(t, "HTTP POST", s.Name())
		a := attrs(s)
		assert.Equal(t, "POST", a["http.request.method"].AsString())
		assert.Equal(t, "127.0.0.1", a["server.address"].AsString())
		assert.Equal(t, int64(503), a["http.response.status_code"].AsInt64())
		assert.Equal(t, codes.Error, s.Status().Code)
		for _, kv := range s.Attributes() {
			assert.NotContains(t, kv.Value.String(), "SECRET", "spans never carry the URL path or query")
		}
	}
	require.Len(t, srv.parent, 2)
	injected := clients[0]
	assert.Equal(t, "00-"+injected.SpanContext().TraceID().String()+"-"+injected.SpanContext().SpanID().String()+"-01",
		srv.parent[0], "inject: the request carries the client span as its parent")
	assert.Empty(t, srv.parent[1], "no injection: no trace headers sent")
}

func TestTransport_NoopProviderSendsNoHeaders(t *testing.T) {
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(noop.NewTracerProvider())
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	srv := &headerServer{}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	resp, err := (&http.Client{Transport: tracing.Transport(nil, true)}).Get(ts.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, []string{""}, srv.parent, "tracing off: nothing injected")
}

func TestTransport_ErrorRecorded(t *testing.T) {
	rec := recordSpans(t)
	base := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("dial refused") })
	_, err := (&http.Client{Transport: tracing.Transport(base, false)}).Get("http://example.invalid/x")
	require.Error(t, err)
	spans := rec.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status().Code)
	assert.Equal(t, "request failed", spans[0].Status().Description)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHandler_LinksInboundTraceWithoutTrustingIt(t *testing.T) {
	rec := recordSpans(t)
	h := tracing.Handler("webhook.scm", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.True(t, trace.SpanFromContext(r.Context()).SpanContext().IsValid(), "the handler runs in the span")
		w.WriteHeader(http.StatusAccepted)
	}))
	req := httptest.NewRequest(http.MethodPost, "/webhook/scm", strings.NewReader("{}"))
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	h.ServeHTTP(httptest.NewRecorder(), req)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/webhook/scm", nil))

	spans := rec.Ended()
	require.Len(t, spans, 2)
	s := spans[0]
	assert.Equal(t, "webhook.scm", s.Name())
	assert.Equal(t, trace.SpanKindServer, s.SpanKind())
	assert.NotEqual(t, "4bf92f3577b34da6a3ce929d0e0e4736", s.SpanContext().TraceID().String(),
		"an unauthenticated caller's trace is not continued")
	assert.False(t, s.Parent().IsValid(), "a new root")
	require.Len(t, s.Links(), 1, "the caller's span is linked")
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", s.Links()[0].SpanContext.TraceID().String())
	assert.Equal(t, "00f067aa0ba902b7", s.Links()[0].SpanContext.SpanID().String())
	assert.Equal(t, int64(202), attrs(s)["http.response.status_code"].AsInt64())
	assert.False(t, spans[1].Parent().IsValid())
	assert.Empty(t, spans[1].Links(), "no traceparent: no link")
}

func TestSanitize(t *testing.T) {
	tests := map[string]string{
		`git clone https://x-access-token:SECRET@github.com/org/repo.git: authentication required`: `git clone https://github.com/…: authentication required`,
		`Post "https://hooks.slack.com/services/T0/B0/TOKEN?x=1": dial tcp: refused`:               `Post "https://hooks.slack.com/…": dial tcp: refused`,
		`GET https://api.github.com/repos/o/r/pulls/7: 404`:                                         `GET https://api.github.com/…: 404`,
		`no url here`:                         `no url here`,
		`root http://forgejo:3000/ ok`:        `root http://forgejo:3000 ok`,
		`ssh://git@git.example.com:22/r.git.`: `ssh://git.example.com:22/….`,
	}
	for in, want := range tests {
		assert.Equal(t, want, tracing.Sanitize(in), in)
	}
}

// TestEnd_RecordsSanitizedErrors: a failed span's status and exception
// event carry the error text with its URLs cut to scheme and host.
func TestEnd_RecordsSanitizedErrors(t *testing.T) {
	rec := recordSpans(t)
	_, span := tracing.Start(context.Background(), "git push")
	tracing.End(span, errors.New("git push https://user:SECRET@git.example.com/org/repo.git: rejected"))
	s := rec.Ended()[0]
	assert.Equal(t, "git push https://git.example.com/…: rejected", s.Status().Description)
	for _, ev := range s.Events() {
		for _, kv := range ev.Attributes {
			assert.NotContains(t, kv.Value.String(), "SECRET")
			assert.NotContains(t, kv.Value.String(), "org/repo")
		}
	}
}

type fakeReconciler struct {
	res ctrl.Result
	err error
	ctx context.Context
}

func (f *fakeReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	f.ctx = ctx
	return f.res, f.err
}

func TestWrapReconciler(t *testing.T) {
	rec := recordSpans(t)
	inner := &fakeReconciler{res: ctrl.Result{RequeueAfter: 30 * time.Second}}
	r := tracing.WrapReconciler("bundle", inner)
	req := reconcile.Request{}
	req.Namespace, req.Name = "team-a", "app-v1"
	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, inner.res, res, "the result passes through")
	assert.True(t, trace.SpanFromContext(inner.ctx).SpanContext().IsValid(), "the reconcile runs in the span")

	inner.err, inner.res = errors.New("conflict"), ctrl.Result{}
	_, err = r.Reconcile(context.Background(), req)
	assert.EqualError(t, err, "conflict", "the error passes through")

	spans := rec.Ended()
	require.Len(t, spans, 2)
	a := attrs(spans[0])
	assert.Equal(t, "bundle.Reconcile", spans[0].Name())
	assert.Equal(t, "team-a", a["k8s.namespace.name"].AsString())
	assert.Equal(t, "app-v1", a["kardinal.object.name"].AsString())
	assert.Equal(t, int64(30000), a["kardinal.requeue_after_ms"].AsInt64())
	assert.Equal(t, codes.Unset, spans[0].Status().Code)
	assert.Equal(t, codes.Error, spans[1].Status().Code)
	assert.Equal(t, "conflict", spans[1].Status().Description)
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		cfg     tracing.Config
		wantErr string
	}{
		{cfg: tracing.Config{}},
		{cfg: tracing.Config{Enabled: false, SamplingRatio: 5}},
		{cfg: tracing.Config{Enabled: true, SamplingRatio: 1, Endpoint: "otel:4318"}},
		{cfg: tracing.Config{Enabled: true, SamplingRatio: 0, Endpoint: "https://otel.example.com"}},
		{cfg: tracing.Config{Enabled: true, SamplingRatio: 1.5}, wantErr: "not between 0 and 1"},
		{cfg: tracing.Config{Enabled: true, SamplingRatio: -0.1}, wantErr: "not between 0 and 1"},
		{cfg: tracing.Config{Enabled: true, SamplingRatio: 1, Endpoint: "grpc://otel:4317"}, wantErr: "not an http:// or https:// URL"},
	}
	for _, tt := range tests {
		err := tt.cfg.Validate()
		if tt.wantErr == "" {
			assert.NoError(t, err, "%+v", tt.cfg)
		} else {
			assert.ErrorContains(t, err, tt.wantErr, "%+v", tt.cfg)
		}
	}
}

func TestSetup_DisabledInstallsNothing(t *testing.T) {
	prev := otel.GetTracerProvider()
	shutdown, err := tracing.Setup(context.Background(), tracing.Config{Enabled: false, Endpoint: "::bad"})
	require.NoError(t, err)
	assert.Equal(t, prev, otel.GetTracerProvider())
	assert.NoError(t, shutdown(context.Background()))
}

// TestSetup_ExportsToOTLPHTTP runs the real exporter against a fake OTLP
// receiver: spans are POSTed to <endpoint>/v1/traces as protobuf, and the
// resource names the service.
func TestSetup_ExportsToOTLPHTTP(t *testing.T) {
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTracerProvider(prevTP); otel.SetTextMapPropagator(prevProp) })

	var mu sync.Mutex
	var paths, types []string
	var bodies [][]byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths, types, bodies = append(paths, r.URL.Path), append(types, r.Header.Get("Content-Type")), append(bodies, b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	shutdown, err := tracing.Setup(context.Background(), tracing.Config{Enabled: true, Endpoint: ts.URL, SamplingRatio: 1,
		ServiceVersion: "v0.10.0-test"})
	require.NoError(t, err)
	_, span := tracing.Start(context.Background(), "export-me")
	span.End()
	require.NoError(t, shutdown(context.Background()))

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, paths)
	assert.Equal(t, "/v1/traces", paths[0])
	assert.Equal(t, "application/x-protobuf", types[0])
	assert.Contains(t, string(bodies[0]), "export-me")
	assert.Contains(t, string(bodies[0]), tracing.ServiceName)
	assert.Contains(t, string(bodies[0]), "v0.10.0-test")
}

func TestHostOf(t *testing.T) {
	assert.Equal(t, "github.com", tracing.HostOf("https://x-access-token:SECRET@github.com/org/repo.git"))
	assert.Equal(t, "", tracing.HostOf("::bad"))
}
