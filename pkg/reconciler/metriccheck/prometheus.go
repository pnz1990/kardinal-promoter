// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
)

// maxResponseBytes bounds the Prometheus response body that is read.
const maxResponseBytes = 1 << 20

// maxErrorText bounds the Prometheus error text copied into an error (and so
// into status.reason).
const maxErrorText = 256

// PrometheusProvider implements MetricsProvider by querying a Prometheus HTTP API.
// It uses the instant query endpoint: GET <prometheusURL>/api/v1/query?query=<promql>.
// A path in prometheusURL is kept as a prefix (Thanos, Mimir, Amazon Managed
// Service for Prometheus workspaces, --web.route-prefix).
//
// Errors never contain the URL or the raw response body: they end up in
// status.reason, which anyone who can read the MetricCheck can see, and the
// URL is user-supplied (C04-gates-15). Only the HTTP status and the error
// field of a Prometheus API error response are reported.
type PrometheusProvider struct {
	// HTTPClient is used for all Prometheus API calls.
	// If nil, the default client from NewPrometheusProvider is used.
	HTTPClient *http.Client
}

// defaultHTTPClient refuses loopback, link-local and cloud metadata
// destinations at dial time (pkg/egress), so a MetricCheck URL cannot reach
// the controller's own UI API or the node's credential endpoints. Private
// ranges stay allowed for in-cluster Prometheus. It honours HTTP(S)_PROXY.
var defaultHTTPClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: egress.NewTransport(http.ProxyFromEnvironment),
}

// NewPrometheusProvider creates a PrometheusProvider with the default,
// egress-guarded HTTP client.
func NewPrometheusProvider() *PrometheusProvider {
	return &PrometheusProvider{HTTPClient: defaultHTTPClient}
}

// QueryScalar calls the Prometheus instant query API and extracts the scalar result.
// Returns an error if the query returns no data, multiple results, or a non-scalar type.
func (p *PrometheusProvider) QueryScalar(ctx context.Context, prometheusURL, query string) (float64, error) {
	base, err := url.Parse(prometheusURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return 0, errors.New("parse prometheus URL: must be an http or https URL with a host")
	}
	endpoint := base.JoinPath("api", "v1", "query")
	q := endpoint.Query()
	q.Set("query", query)
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, errors.New("build prometheus request: invalid URL")
	}

	hc := p.HTTPClient
	if hc == nil {
		hc = defaultHTTPClient
	}

	resp, err := hc.Do(req)
	if err != nil {
		// *url.Error repeats the full URL, which may carry credentials.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			if uerr.Timeout() {
				return 0, errors.New("prometheus GET timed out")
			}
			return 0, fmt.Errorf("prometheus GET: %w", uerr.Err)
		}
		return 0, fmt.Errorf("prometheus GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var apiResp prometheusQueryResponse
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&apiResp)

	if resp.StatusCode != http.StatusOK {
		// Prometheus answers 400/422/503 with a JSON error envelope. Report
		// its error field; any other body is not reflected.
		if decodeErr == nil && apiResp.Status == "error" && apiResp.ErrorType != "" {
			return 0, fmt.Errorf("prometheus returned HTTP %d: %s: %s",
				resp.StatusCode, apiResp.ErrorType, truncate(apiResp.Error))
		}
		return 0, fmt.Errorf("prometheus returned HTTP %d", resp.StatusCode)
	}
	if decodeErr != nil {
		return 0, errors.New("decode prometheus response: not a Prometheus API response")
	}

	if apiResp.Status != "success" {
		return 0, fmt.Errorf("prometheus query error: %s", truncate(apiResp.Error))
	}

	return extractScalar(apiResp.Data)
}

// truncate shortens s to maxErrorText bytes on a rune boundary.
func truncate(s string) string {
	if len(s) <= maxErrorText {
		return s
	}
	cut := maxErrorText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// prometheusQueryResponse is the Prometheus API response envelope.
type prometheusQueryResponse struct {
	Status    string                `json:"status"`
	ErrorType string                `json:"errorType,omitempty"`
	Error     string                `json:"error,omitempty"`
	Data      prometheusQueryResult `json:"data"`
}

// prometheusQueryResult holds the result type and the vector/scalar values.
type prometheusQueryResult struct {
	ResultType string          `json:"resultType"`
	Result     json.RawMessage `json:"result"`
}

// extractScalar extracts a single float64 from a Prometheus query result.
// Supports resultType "scalar" and "vector" (single-element).
func extractScalar(data prometheusQueryResult) (float64, error) {
	switch data.ResultType {
	case "scalar":
		// scalar result: [unix_timestamp, "value_string"]
		var scalar [2]json.RawMessage
		if err := json.Unmarshal(data.Result, &scalar); err != nil {
			return 0, fmt.Errorf("parse scalar result: %w", err)
		}
		var valStr string
		if err := json.Unmarshal(scalar[1], &valStr); err != nil {
			return 0, fmt.Errorf("parse scalar value: %w", err)
		}
		v, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			return 0, fmt.Errorf("parse scalar float: %w", err)
		}
		return v, nil

	case "vector":
		// vector result: [{metric:{}, value:[ts, "val"]}]
		var vector []struct {
			Value [2]json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(data.Result, &vector); err != nil {
			return 0, fmt.Errorf("parse vector result: %w", err)
		}
		if len(vector) == 0 {
			return 0, fmt.Errorf("prometheus query returned empty vector")
		}
		if len(vector) > 1 {
			return 0, fmt.Errorf("prometheus query returned %d series, expected 1", len(vector))
		}
		var valStr string
		if err := json.Unmarshal(vector[0].Value[1], &valStr); err != nil {
			return 0, fmt.Errorf("parse vector value: %w", err)
		}
		v, err := strconv.ParseFloat(valStr, 64)
		if err != nil {
			return 0, fmt.Errorf("parse vector float: %w", err)
		}
		return v, nil

	default:
		return 0, fmt.Errorf("unsupported prometheus resultType %q", data.ResultType)
	}
}
