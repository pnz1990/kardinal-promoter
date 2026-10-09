// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// defaultDatadogSite is the Datadog site when spec.datadog.site is empty.
const defaultDatadogSite = "datadoghq.com"

// DatadogProvider queries the Datadog metrics query API:
// GET <address>/api/v1/query?from=<now-window>&to=<now>&query=<query>, with
// the DD-API-KEY and DD-APPLICATION-KEY headers. The query must return one
// series; the value is its latest non-null point.
type DatadogProvider struct {
	// HTTPClient is used for the API calls; nil means the egress-guarded
	// default client.
	HTTPClient *http.Client
}

// datadogResponse is the part of the v1 query response that is read.
type datadogResponse struct {
	Status string   `json:"status"`
	Error  string   `json:"error"`
	Errors []string `json:"errors"`
	Series []struct {
		Pointlist [][]*float64 `json:"pointlist"`
	} `json:"series"`
}

// Evaluate implements Backend.
func (p *DatadogProvider) Evaluate(ctx context.Context, q Query) (Value, error) {
	dd := q.Spec.Datadog
	if dd == nil {
		return Value{}, errors.New("datadog: spec.datadog is required")
	}
	address := dd.Address
	if address == "" {
		site := dd.Site
		if site == "" {
			site = defaultDatadogSite
		}
		address = "https://api." + site
	}
	base, err := baseURL(address, "datadog address")
	if err != nil {
		return Value{}, err
	}
	apiKey, err := q.Secret(ctx, dd.APIKeySecretRef)
	if err != nil {
		return Value{}, fmt.Errorf("datadog API key: %w", err)
	}
	appKey, err := q.Secret(ctx, dd.ApplicationKeySecretRef)
	if err != nil {
		return Value{}, fmt.Errorf("datadog application key: %w", err)
	}

	endpoint := base.JoinPath("api", "v1", "query")
	params := endpoint.Query()
	params.Set("from", strconv.FormatInt(q.Now.Add(-parseWindow(dd.Window)).Unix(), 10))
	params.Set("to", strconv.FormatInt(q.Now.Unix(), 10))
	params.Set("query", q.Spec.Query)
	endpoint.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Value{}, errors.New("build datadog request: invalid URL")
	}
	req.Header.Set("DD-API-KEY", apiKey)
	req.Header.Set("DD-APPLICATION-KEY", appKey)
	req.Header.Set("Accept", "application/json")

	status, body, err := do(p.HTTPClient, req, "datadog")
	if err != nil {
		return Value{}, err
	}
	var resp datadogResponse
	decodeErr := json.Unmarshal(body, &resp)
	if status != http.StatusOK {
		if decodeErr == nil && len(resp.Errors) > 0 {
			return Value{}, fmt.Errorf("datadog returned HTTP %d: %s", status, truncate(strings.Join(resp.Errors, "; ")))
		}
		return Value{}, fmt.Errorf("datadog returned HTTP %d", status)
	}
	if decodeErr != nil {
		return Value{}, errors.New("decode datadog response: not a Datadog query response")
	}
	if resp.Status == "error" {
		return Value{}, fmt.Errorf("datadog query error: %s", truncate(resp.Error))
	}
	return datadogValue(resp)
}

// datadogValue returns the latest non-null point of the single series.
func datadogValue(resp datadogResponse) (Value, error) {
	switch n := len(resp.Series); {
	case n == 0:
		return Value{}, errors.New("datadog query returned no series")
	case n > 1:
		return Value{}, fmt.Errorf("datadog query returned %d series, expected 1", n)
	}
	points := resp.Series[0].Pointlist
	for i := len(points) - 1; i >= 0; i-- {
		if len(points[i]) == 2 && points[i][1] != nil {
			return NumberValue(*points[i][1]), nil
		}
	}
	return Value{}, errors.New("datadog query returned no points")
}
