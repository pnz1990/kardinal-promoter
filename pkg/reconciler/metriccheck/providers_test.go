// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
)

// plainClient reaches httptest servers on loopback, which the egress-guarded
// default clients refuse.
var plainClient = &http.Client{Timeout: 10 * time.Second}

// secrets is a Query.Secret over a fixed map "<name>/<key>" → value.
func secrets(m map[string]string) func(context.Context, kardinalv1alpha1.SecretKeyRef) (string, error) {
	return func(_ context.Context, ref kardinalv1alpha1.SecretKeyRef) (string, error) {
		v, ok := m[ref.Name+"/"+ref.Key]
		if !ok {
			return "", fmt.Errorf("secret %q has no key %q", ref.Name, ref.Key)
		}
		return v, nil
	}
}

// recorder is an httptest handler that records the last request and answers
// status and body.
type recorder struct {
	mu      sync.Mutex
	status  int
	body    string
	req     *http.Request
	reqBody string
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.req, r.reqBody = req, string(b)
	status, body := r.status, r.body
	r.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func serve(t *testing.T, status int, body string) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{status: status, body: body}
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	return srv, rec
}

var queryNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func TestDatadogProvider(t *testing.T) {
	keys := secrets(map[string]string{"dd/api": "API-SECRET", "dd/app": "APP-SECRET"})
	spec := func(address string) *kardinalv1alpha1.MetricCheckSpec {
		return &kardinalv1alpha1.MetricCheckSpec{
			Provider: "datadog", Query: "avg:errors{version:1.2.3}",
			Datadog: &kardinalv1alpha1.DatadogProviderSpec{
				Address: address, Window: "10m",
				APIKeySecretRef:         kardinalv1alpha1.SecretKeyRef{Name: "dd", Key: "api"},
				ApplicationKeySecretRef: kardinalv1alpha1.SecretKeyRef{Name: "dd", Key: "app"},
			},
		}
	}
	tests := []struct {
		name    string
		status  int
		body    string
		want    float64
		wantErr string
	}{
		{"latest non-null point", 200, `{"status":"ok","series":[{"pointlist":[[1,0.5],[2,0.25],[3,null]]}]}`, 0.25, ""},
		{"no series", 200, `{"status":"ok","series":[]}`, 0, "no series"},
		{"two series", 200, `{"status":"ok","series":[{"pointlist":[[1,1]]},{"pointlist":[[1,2]]}]}`, 0, "2 series"},
		{"only nulls", 200, `{"status":"ok","series":[{"pointlist":[[1,null]]}]}`, 0, "no points"},
		{"query error", 200, `{"status":"error","error":"bad query"}`, 0, "datadog query error: bad query"},
		{"forbidden", 403, `{"errors":["Forbidden"]}`, 0, "HTTP 403: Forbidden"},
		{"html", 502, `<html>API-SECRET</html>`, 0, "HTTP 502"},
		{"not json", 200, `nope`, 0, "not a Datadog query response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, rec := serve(t, tt.status, tt.body)
			p := &metriccheck.DatadogProvider{HTTPClient: plainClient}
			v, err := p.Evaluate(context.Background(), metriccheck.Query{Spec: spec(srv.URL + "/dd"), Secret: keys, Now: queryNow})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.NotContains(t, err.Error(), "SECRET", "no credential in the error")
				assert.NotContains(t, err.Error(), srv.URL, "no URL in the error")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, v.Number)
			assert.Equal(t, "/dd/api/v1/query", rec.req.URL.Path)
			q := rec.req.URL.Query()
			assert.Equal(t, "avg:errors{version:1.2.3}", q.Get("query"))
			assert.Equal(t, fmt.Sprint(queryNow.Add(-10*time.Minute).Unix()), q.Get("from"))
			assert.Equal(t, fmt.Sprint(queryNow.Unix()), q.Get("to"))
			assert.Equal(t, "API-SECRET", rec.req.Header.Get("DD-API-KEY"))
			assert.Equal(t, "APP-SECRET", rec.req.Header.Get("DD-APPLICATION-KEY"))
		})
	}

	t.Run("missing secret", func(t *testing.T) {
		p := &metriccheck.DatadogProvider{HTTPClient: plainClient}
		_, err := p.Evaluate(context.Background(), metriccheck.Query{Spec: spec("http://x"), Secret: secrets(nil), Now: queryNow})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "datadog API key")
	})
	t.Run("default site", func(t *testing.T) {
		var host string
		hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			host = r.URL.Host
			return nil, errors.New("stop")
		})}
		s := spec("")
		s.Datadog.Site = "datadoghq.eu"
		_, err := (&metriccheck.DatadogProvider{HTTPClient: hc}).Evaluate(context.Background(),
			metriccheck.Query{Spec: s, Secret: keys, Now: queryNow})
		require.Error(t, err)
		assert.Equal(t, "api.datadoghq.eu", host)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNewRelicProvider(t *testing.T) {
	keys := secrets(map[string]string{"nr/key": "NRAK-SECRET"})
	spec := func(address, field string) *kardinalv1alpha1.MetricCheckSpec {
		return &kardinalv1alpha1.MetricCheckSpec{
			Provider: "newrelic", Query: "SELECT percentage(count(*), WHERE error IS true) FROM Transaction",
			NewRelic: &kardinalv1alpha1.NewRelicProviderSpec{
				AccountID: 1234567, Address: address, ResultField: field,
				APIKeySecretRef: kardinalv1alpha1.SecretKeyRef{Name: "nr", Key: "key"},
			},
		}
	}
	rows := func(r string) string {
		return `{"data":{"actor":{"account":{"nrql":{"results":` + r + `}}}}}`
	}
	tests := []struct {
		name, field, body, wantErr string
		status                     int
		want                       float64
	}{
		{name: "single field", body: rows(`[{"percentage":1.5}]`), want: 1.5},
		{name: "named field", field: "count", body: rows(`[{"count":7,"other":"x"}]`), want: 7},
		{name: "ambiguous row", body: rows(`[{"a":1,"b":2}]`), wantErr: "fields a, b: set newRelic.resultField"},
		{name: "missing field", field: "nope", body: rows(`[{"a":1}]`), wantErr: `no field "nope"`},
		{name: "not a number", body: rows(`[{"a":"x"}]`), wantErr: "not a number"},
		{name: "no rows", body: rows(`[]`), wantErr: "no rows"},
		{name: "two rows", body: rows(`[{"a":1},{"a":2}]`), wantErr: "2 rows"},
		{name: "graphql error", body: `{"errors":[{"message":"NRQL Syntax error"}]}`, wantErr: "newrelic query error: NRQL Syntax error"},
		{name: "unauthorized", status: 401, body: `{"errors":[{"message":"Invalid API key"}]}`, wantErr: "HTTP 401: Invalid API key"},
		{name: "no account", body: `{"data":{"actor":{"account":null}}}`, wantErr: "no result"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, rec := serve(t, tt.status, tt.body)
			v, err := (&metriccheck.NewRelicProvider{HTTPClient: plainClient}).Evaluate(context.Background(),
				metriccheck.Query{Spec: spec(srv.URL+"/graphql", tt.field), Secret: keys, Now: queryNow})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.NotContains(t, err.Error(), "NRAK-SECRET")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, v.Number)
			assert.Equal(t, http.MethodPost, rec.req.Method)
			assert.Equal(t, "NRAK-SECRET", rec.req.Header.Get("API-Key"))
			var sent struct {
				Query     string                 `json:"query"`
				Variables map[string]interface{} `json:"variables"`
			}
			require.NoError(t, json.Unmarshal([]byte(rec.reqBody), &sent))
			assert.Contains(t, sent.Query, "nrql(query: $nrql)", "the NRQL goes in as a variable")
			assert.Equal(t, float64(1234567), sent.Variables["accountId"])
			assert.Equal(t, spec("", "").Query, sent.Variables["nrql"])
		})
	}
}

func TestCloudWatchProvider(t *testing.T) {
	keys := secrets(map[string]string{"aws/id": "AKIDEXAMPLE", "aws/secret": "wJalrXUtnFEMI-SECRET", "aws/token": "TOKEN"})
	spec := func(endpoint string, creds bool) *kardinalv1alpha1.MetricCheckSpec {
		cw := &kardinalv1alpha1.CloudWatchProviderSpec{Region: "eu-west-1", Endpoint: endpoint, Period: 30}
		if creds {
			cw.AccessKeyIDSecretRef = &kardinalv1alpha1.SecretKeyRef{Name: "aws", Key: "id"}
			cw.SecretAccessKeySecretRef = &kardinalv1alpha1.SecretKeyRef{Name: "aws", Key: "secret"}
			cw.SessionTokenSecretRef = &kardinalv1alpha1.SecretKeyRef{Name: "aws", Key: "token"}
		}
		return &kardinalv1alpha1.MetricCheckSpec{Provider: "cloudwatch",
			Query: `SELECT AVG(Errors) FROM SCHEMA("AWS/Lambda", FunctionName)`, CloudWatch: cw}
	}
	result := func(members string) string {
		return `<GetMetricDataResponse xmlns="http://monitoring.amazonaws.com/doc/2010-08-01/"><GetMetricDataResult>` +
			`<MetricDataResults>` + members + `</MetricDataResults><Messages/></GetMetricDataResult></GetMetricDataResponse>`
	}
	one := `<member><Id>kardinal</Id><StatusCode>Complete</StatusCode>` +
		`<Timestamps><member>2026-10-08T11:58:00Z</member><member>2026-10-08T11:59:00Z</member></Timestamps>` +
		`<Values><member>3</member><member>4.5</member></Values></member>`
	tests := []struct {
		name, body, wantErr string
		status              int
		want                float64
	}{
		{name: "latest point", body: result(one), want: 4.5},
		{name: "no results", body: result(``), wantErr: "no results"},
		{name: "two results", body: result(one + one), wantErr: "2 results"},
		{name: "no points", body: result(`<member><Id>kardinal</Id><Timestamps/><Values/></member>`), wantErr: "no points"},
		{name: "api error", status: 400, wantErr: "HTTP 400: InvalidParameterValue: bad expression",
			body: `<ErrorResponse><Error><Type>Sender</Type><Code>InvalidParameterValue</Code><Message>bad expression</Message></Error></ErrorResponse>`},
		{name: "messages", wantErr: "cloudwatch query error: MaxQueryTimeRangeExceed",
			body: `<GetMetricDataResponse><GetMetricDataResult><MetricDataResults/><Messages><member><Code>MaxQueryTimeRangeExceed</Code><Value>too long</Value></member></Messages></GetMetricDataResult></GetMetricDataResponse>`},
		{name: "not xml", body: `{}`, wantErr: "not a GetMetricData response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, rec := serve(t, tt.status, tt.body)
			v, err := (&metriccheck.CloudWatchProvider{HTTPClient: plainClient}).Evaluate(context.Background(),
				metriccheck.Query{Spec: spec(srv.URL, true), Secret: keys, Now: queryNow})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.NotContains(t, err.Error(), "SECRET")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, v.Number)
			form, err := url.ParseQuery(rec.reqBody)
			require.NoError(t, err)
			assert.Equal(t, "GetMetricData", form.Get("Action"))
			assert.Equal(t, "2010-08-01", form.Get("Version"))
			assert.Equal(t, spec("", false).Query, form.Get("MetricDataQueries.member.1.Expression"))
			assert.Equal(t, "30", form.Get("MetricDataQueries.member.1.Period"))
			assert.Equal(t, "2026-10-08T11:55:00Z", form.Get("StartTime"))
			assert.Equal(t, "2026-10-08T12:00:00Z", form.Get("EndTime"))
			auth := rec.req.Header.Get("Authorization")
			assert.True(t, strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261008/eu-west-1/monitoring/aws4_request"), auth)
			assert.NotContains(t, auth, "SECRET")
			assert.Equal(t, "TOKEN", rec.req.Header.Get("X-Amz-Security-Token"))
		})
	}

	t.Run("no credentials and no ambient identity", func(t *testing.T) {
		_, err := (&metriccheck.CloudWatchProvider{HTTPClient: plainClient}).Evaluate(context.Background(),
			metriccheck.Query{Spec: spec("http://x", false), Secret: keys, Now: queryNow})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--metriccheck-cloudwatch-ambient-credentials")
	})
	t.Run("ambient identity from the environment", func(t *testing.T) {
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIDAMBIENT")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
		t.Setenv("AWS_CONFIG_FILE", "/dev/null")
		t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
		t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
		t.Setenv("AWS_PROFILE", "")
		t.Setenv("AWS_DEFAULT_PROFILE", "")
		t.Setenv("AWS_SESSION_TOKEN", "")
		t.Setenv("AWS_ROLE_ARN", "")
		t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "")
		srv, rec := serve(t, 200, result(one))
		// The endpoint must be an AWS host with ambient credentials; the
		// client sends the request to the fake instead.
		target, err := url.Parse(srv.URL)
		require.NoError(t, err)
		toFake := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
			return http.DefaultTransport.RoundTrip(r)
		})}
		p := &metriccheck.CloudWatchProvider{HTTPClient: toFake, AmbientCredentials: true}
		v, err := p.Evaluate(context.Background(),
			metriccheck.Query{Spec: spec("https://vpce-0a1b-xyz.monitoring.eu-west-1.vpce.amazonaws.com", false), Secret: keys, Now: queryNow})
		require.NoError(t, err)
		assert.Equal(t, 4.5, v.Number)
		assert.Contains(t, rec.req.Header.Get("Authorization"), "Credential=AKIDAMBIENT/")

		// QA #1479: the controller's own credentials never go to another host.
		for _, endpoint := range []string{srv.URL, "https://evil.example.com", "http://monitoring.eu-west-1.amazonaws.com", "https://s3.eu-west-1.amazonaws.com",
			"https://amazonaws.com.evil.example"} {
			before := rec.req
			_, err := p.Evaluate(context.Background(),
				metriccheck.Query{Spec: spec(endpoint, false), Secret: keys, Now: queryNow})
			require.Error(t, err, endpoint)
			assert.Contains(t, err.Error(), "must be an https monitoring.<region>.amazonaws.com(.cn) host", endpoint)
			assert.Same(t, before, rec.req, "%s: nothing was sent", endpoint)
		}
	})
	t.Run("default endpoint is regional and egress guarded", func(t *testing.T) {
		var host string
		hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			host = r.URL.Host
			return nil, errors.New("stop")
		})}
		_, err := (&metriccheck.CloudWatchProvider{HTTPClient: hc}).Evaluate(context.Background(),
			metriccheck.Query{Spec: spec("", true), Secret: keys, Now: queryNow})
		require.Error(t, err)
		assert.Equal(t, "monitoring.eu-west-1.amazonaws.com", host)
	})
}

func TestWebProvider(t *testing.T) {
	keys := secrets(map[string]string{"web/token": "Bearer WEB-SECRET"})
	hdr := "1.2.3"
	spec := func(url, method, path string) *kardinalv1alpha1.MetricCheckSpec {
		return &kardinalv1alpha1.MetricCheckSpec{Provider: "web", Web: &kardinalv1alpha1.WebProviderSpec{
			URL: url, Method: method, JSONPath: path, Body: map[bool]string{true: `{"v":"1.2.3"}`}[method == "POST"],
			Headers: []kardinalv1alpha1.WebHeader{
				{Name: "Authorization", ValueFromSecret: &kardinalv1alpha1.SecretKeyRef{Name: "web", Key: "token"}},
				{Name: "X-Version", Value: &hdr},
			},
		}}
	}
	tests := []struct {
		name, method, path, body, wantErr, wantText string
		status                                      int
		wantNum                                     float64
		numeric                                     bool
	}{
		{name: "number", path: "{.data.rate}", body: `{"data":{"rate":0.25}}`, wantNum: 0.25, numeric: true, wantText: "0.25"},
		{name: "big int keeps text", path: "{.n}", body: `{"n":12345678901234567890}`, wantNum: 12345678901234567890, numeric: true, wantText: "12345678901234567890"},
		{name: "numeric string", method: "POST", path: "{.v}", body: `{"v":"42"}`, wantNum: 42, numeric: true, wantText: "42"},
		{name: "text", path: "{.status}", body: `{"status":"healthy"}`, wantText: "healthy"},
		{name: "bool", path: "{.ok}", body: `{"ok":true}`, wantText: "true"},
		{name: "list element", path: "{.checks[1].state}", body: `{"checks":[{"state":"a"},{"state":"b"}]}`, wantText: "b"},
		{name: "nothing selected", path: "{.missing}", body: `{"a":"SECRET-DOC"}`, wantErr: "web jsonPath {.missing} selected nothing"},
		// QA #1479: recursive descent can take seconds of CPU on a deep document.
		{name: "recursive descent refused", path: "{..a}", body: `{"a":1}`, wantErr: "recursive descent (..) is not supported"},
		{name: "invalid expression", path: "{.a[}", body: `{"a":1}`, wantErr: "not a valid JSONPath expression"},
		{name: "many selected", path: "{.items[*].v}", body: `{"items":[{"v":1},{"v":2}]}`, wantErr: "selected 2 values"},
		{name: "object selected", path: "{.a}", body: `{"a":{"b":1}}`, wantErr: "object or a list"},
		{name: "null selected", path: "{.a}", body: `{"a":null}`, wantErr: "null"},
		{name: "not json", path: "{.a}", body: `<html>`, wantErr: "not JSON"},
		{name: "server error", status: 503, path: "{.a}", body: `{"a":1}`, wantErr: "web returned HTTP 503"},
		{name: "redirect not followed", status: 302, path: "{.a}", body: ``, wantErr: "web returned HTTP 302"},
		{name: "path without braces", path: ".a", body: `{"a":1}`, wantErr: "must be one {...} expression"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, rec := serve(t, tt.status, tt.body)
			p := &metriccheck.WebProvider{HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}}}
			v, err := p.Evaluate(context.Background(), metriccheck.Query{
				Spec: spec(srv.URL+"/check?v=1.2.3", tt.method, tt.path), Secret: keys, Now: queryNow})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.NotContains(t, err.Error(), "WEB-SECRET")
				assert.NotContains(t, err.Error(), srv.URL)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.numeric, v.Numeric)
			assert.Equal(t, tt.wantText, v.Text)
			if tt.numeric {
				assert.Equal(t, tt.wantNum, v.Number)
			}
			assert.Equal(t, "Bearer WEB-SECRET", rec.req.Header.Get("Authorization"))
			assert.Equal(t, "1.2.3", rec.req.Header.Get("X-Version"))
			assert.Equal(t, "1.2.3", rec.req.URL.Query().Get("v"))
			if tt.method == "POST" {
				assert.Equal(t, http.MethodPost, rec.req.Method)
				assert.Equal(t, `{"v":"1.2.3"}`, rec.reqBody)
			}
		})
	}

	t.Run("timeout", func(t *testing.T) {
		block := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(block) })
		s := spec(srv.URL, "GET", "{.a}")
		s.Web.TimeoutSeconds = 1
		_, err := (&metriccheck.WebProvider{HTTPClient: plainClient}).Evaluate(context.Background(),
			metriccheck.Query{Spec: s, Secret: keys, Now: queryNow})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timed out")
	})
	t.Run("missing secret", func(t *testing.T) {
		_, err := (&metriccheck.WebProvider{HTTPClient: plainClient}).Evaluate(context.Background(),
			metriccheck.Query{Spec: spec("http://x", "GET", "{.a}"), Secret: secrets(nil), Now: queryNow})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "web header Authorization")
	})
}

// TestDefaultBackends_RefuseLoopback: every provider's default client goes
// through the egress guard, so a MetricCheck URL cannot reach the
// controller's own loopback API (or link-local credential endpoints).
func TestDefaultBackends_RefuseLoopback(t *testing.T) {
	srv, _ := serve(t, 200, `{}`)
	keys := secrets(map[string]string{"s/k": "v"})
	ref := kardinalv1alpha1.SecretKeyRef{Name: "s", Key: "k"}
	specs := map[string]*kardinalv1alpha1.MetricCheckSpec{
		"prometheus": {Provider: "prometheus", PrometheusURL: srv.URL, Query: "up"},
		"datadog": {Provider: "datadog", Query: "q", Datadog: &kardinalv1alpha1.DatadogProviderSpec{
			Address: srv.URL, APIKeySecretRef: ref, ApplicationKeySecretRef: ref}},
		"cloudwatch": {Provider: "cloudwatch", Query: "q", CloudWatch: &kardinalv1alpha1.CloudWatchProviderSpec{
			Region: "us-east-1", Endpoint: srv.URL, AccessKeyIDSecretRef: &ref, SecretAccessKeySecretRef: &ref}},
		"newrelic": {Provider: "newrelic", Query: "q", NewRelic: &kardinalv1alpha1.NewRelicProviderSpec{
			AccountID: 1, Address: srv.URL, APIKeySecretRef: ref}},
		"web": {Provider: "web", Web: &kardinalv1alpha1.WebProviderSpec{URL: srv.URL, JSONPath: "{.a}"}},
	}
	backends := metriccheck.DefaultBackends(false)
	require.Len(t, backends, len(specs))
	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			_, err := backends[name].Evaluate(context.Background(), metriccheck.Query{Spec: spec, Secret: keys, Now: queryNow})
			require.Error(t, err)
			assert.True(t, errors.Is(err, egress.ErrBlockedAddress), "%s: %v", name, err)
		})
	}
}

// TestWebProvider_CostBounds covers the QA findings on #1479: the response is
// capped at 64 KiB, a JSONPath error never quotes the document, and a
// request never outlives half the interval.
func TestWebProvider_CostBounds(t *testing.T) {
	spec := func(url, path string) *kardinalv1alpha1.MetricCheckSpec {
		return &kardinalv1alpha1.MetricCheckSpec{Provider: "web", Interval: "10s",
			Web: &kardinalv1alpha1.WebProviderSpec{URL: url, JSONPath: path, TimeoutSeconds: 60}}
	}
	eval := func(p *metriccheck.WebProvider, s *kardinalv1alpha1.MetricCheckSpec) error {
		_, err := p.Evaluate(context.Background(), metriccheck.Query{Spec: s, Secret: secrets(nil), Now: queryNow})
		return err
	}

	t.Run("body cap", func(t *testing.T) {
		srv, _ := serve(t, 200, `{"a":"`+strings.Repeat("x", 64<<10)+`"}`)
		err := eval(&metriccheck.WebProvider{HTTPClient: plainClient}, spec(srv.URL, "{.a}"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "response larger than 65536 bytes")
	})
	t.Run("jsonpath error does not quote the document", func(t *testing.T) {
		srv, _ := serve(t, 200, `{"a":{"k":"SECRET-DOC"}}`)
		err := eval(&metriccheck.WebProvider{HTTPClient: plainClient}, spec(srv.URL, "{.a[0]}"))
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "SECRET-DOC")
		assert.Equal(t, "web jsonPath {.a[0]} selected nothing", err.Error())
	})
	t.Run("timeout is under the interval", func(t *testing.T) {
		block := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(block) })
		start := time.Now()
		err := eval(&metriccheck.WebProvider{HTTPClient: plainClient}, spec(srv.URL, "{.a}"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timed out")
		assert.Less(t, time.Since(start), 8*time.Second, "timeoutSeconds 60 is cut to half the 10s interval")
	})
}
