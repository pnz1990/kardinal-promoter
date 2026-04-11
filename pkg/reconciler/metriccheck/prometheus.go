// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// PrometheusProvider implements MetricsProvider by querying a Prometheus HTTP API.
// It uses the instant query endpoint: GET /api/v1/query?query=<promql>
type PrometheusProvider struct {
	// HTTPClient is used for all Prometheus API calls.
	// If nil, http.DefaultClient is used.
	HTTPClient *http.Client
}

// NewPrometheusProvider creates a PrometheusProvider with a default HTTP client.
func NewPrometheusProvider() *PrometheusProvider {
	return &PrometheusProvider{
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// QueryScalar calls the Prometheus instant query API and extracts the scalar result.
// Returns an error if the query returns no data, multiple results, or a non-scalar type.
func (p *PrometheusProvider) QueryScalar(ctx context.Context, prometheusURL, query string) (float64, error) {
	endpoint, err := url.Parse(prometheusURL)
	if err != nil {
		return 0, fmt.Errorf("parse prometheus URL: %w", err)
	}
	endpoint.Path = "/api/v1/query"

	q := endpoint.Query()
	q.Set("query", query)
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}

	hc := p.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	resp, err := hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("prometheus GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("prometheus returned HTTP %d: %s", resp.StatusCode, body)
	}

	var apiResp prometheusQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return 0, fmt.Errorf("decode prometheus response: %w", err)
	}

	if apiResp.Status != "success" {
		return 0, fmt.Errorf("prometheus query error: %s", apiResp.Error)
	}

	return extractScalar(apiResp.Data)
}

// prometheusQueryResponse is the Prometheus API response envelope.
type prometheusQueryResponse struct {
	Status string                `json:"status"`
	Error  string                `json:"error,omitempty"`
	Data   prometheusQueryResult `json:"data"`
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
