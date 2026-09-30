// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
)

const okVector = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"0.5"]}]}}`

// TestQueryScalar_PathPrefixKept covers C04-gates-16: Prometheus behind a
// path prefix (Thanos, Mimir, AMP workspaces, --web.route-prefix) is queried
// at <prefix>/api/v1/query, not at /api/v1/query on the host.
func TestQueryScalar_PathPrefixKept(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		wantPath string
	}{
		{name: "no path", prefix: "", wantPath: "/api/v1/query"},
		{name: "trailing slash", prefix: "/", wantPath: "/api/v1/query"},
		{name: "prefix", prefix: "/prometheus", wantPath: "/prometheus/api/v1/query"},
		{name: "prefix with slash", prefix: "/workspaces/ws-1/", wantPath: "/workspaces/ws-1/api/v1/query"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotQuery string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotQuery = r.URL.Path, r.URL.Query().Get("query")
				_, _ = w.Write([]byte(okVector))
			}))
			defer srv.Close()

			v, err := loopbackProvider().QueryScalar(context.Background(), srv.URL+tt.prefix, `up{job="a"}`)
			require.NoError(t, err)
			assert.InDelta(t, 0.5, v, 1e-9)
			assert.Equal(t, tt.wantPath, gotPath)
			assert.Equal(t, `up{job="a"}`, gotQuery)
		})
	}
}

// TestQueryScalar_ErrorsDoNotLeak covers C04-gates-15: the error becomes
// status.reason, readable by anyone who can read the MetricCheck. It must not
// contain the response body of an arbitrary endpoint, nor the URL (which may
// carry credentials), but it does report the HTTP status and the error of a
// Prometheus API error response.
func TestQueryScalar_ErrorsDoNotLeak(t *testing.T) {
	serve := func(code int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		}))
	}
	forbidden := serve(http.StatusForbidden, "internal-only: db_password=hunter2")
	defer forbidden.Close()
	htmlOK := serve(http.StatusOK, "<html>admin console hunter2</html>")
	defer htmlOK.Close()
	badData := serve(http.StatusBadRequest, `{"status":"error","errorType":"bad_data","error":"parse error at char 5"}`)
	defer badData.Close()
	unprocessable := serve(http.StatusUnprocessableEntity, `{"status":"error","errorType":"execution","error":"`+strings.Repeat("x", 2000)+`"}`)
	defer unprocessable.Close()
	closed := serve(http.StatusOK, okVector)
	closedURL := strings.Replace(closed.URL, "http://", "http://admin:hunter2@", 1) + "/p?token=hunter2"
	closed.Close()

	tests := []struct {
		name       string
		url        string
		wantIn     []string
		wantMaxLen int
	}{
		{name: "non-Prometheus error body", url: forbidden.URL, wantIn: []string{"HTTP 403"}},
		{name: "non-Prometheus 200 body", url: htmlOK.URL, wantIn: []string{"not a Prometheus API response"}},
		{name: "Prometheus API error", url: badData.URL, wantIn: []string{"HTTP 400", "bad_data", "parse error at char 5"}},
		{name: "long Prometheus error is truncated", url: unprocessable.URL, wantIn: []string{"HTTP 422", "execution"}, wantMaxLen: 400},
		{name: "credentials in URL, connection refused", url: closedURL, wantIn: []string{"prometheus GET"}},
		{name: "credentials in unparseable URL", url: "http://admin:hunter2@[::1/", wantIn: []string{"parse prometheus URL"}},
		{name: "non-http scheme", url: "file:///etc/hunter2", wantIn: []string{"http or https"}},
		{name: "no host", url: "http:///hunter2", wantIn: []string{"http or https"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loopbackProvider().QueryScalar(context.Background(), tt.url, "up")
			require.Error(t, err)
			for _, want := range tt.wantIn {
				assert.Contains(t, err.Error(), want)
			}
			assert.NotContains(t, err.Error(), "hunter2")
			if tt.wantMaxLen > 0 {
				assert.LessOrEqual(t, len(err.Error()), tt.wantMaxLen)
			}
		})
	}
}

// TestQueryScalar_BodyIsBounded covers C04-gates-17: the response body is read
// through a limit, so an endpoint that streams an unbounded body cannot hold
// the reconcile or grow memory without bound.
func TestQueryScalar_BodyIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":["`))
		chunk := []byte(strings.Repeat("a", 64<<10))
		for i := 0; i < 64; i++ { // 4 MiB, well past the 1 MiB limit
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	_, err := loopbackProvider().QueryScalar(context.Background(), srv.URL, "up")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a Prometheus API response")
}

// TestReconciler_MinimumInterval covers C04-gates-17: an interval below 10s is
// raised to 10s, so a MetricCheck cannot make the controller query Prometheus
// in a tight loop.
func TestReconciler_MinimumInterval(t *testing.T) {
	tests := []struct {
		interval string
		want     time.Duration
	}{
		{interval: "1ms", want: 10 * time.Second},
		{interval: "0s", want: time.Minute}, // zero or invalid: the default
		{interval: "9s", want: 10 * time.Second},
		{interval: "10s", want: 10 * time.Second},
		{interval: "45s", want: 45 * time.Second},
		{interval: "", want: time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.interval, func(t *testing.T) {
			mc := newMetricCheck("fast", "lt", 1)
			mc.Spec.Interval = tt.interval
			c := fake.NewClientBuilder().WithScheme(buildScheme()).WithStatusSubresource(mc).WithObjects(mc).Build()
			r := &metriccheck.Reconciler{Client: c, Provider: &fakeProvider{value: 0.5}, NowFn: func() time.Time { return fixedNow }}

			res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(mc)})
			require.NoError(t, err)
			assert.Equal(t, tt.want, res.RequeueAfter)
		})
	}
}
