// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// NerdGraph endpoints by region.
const (
	newRelicUSEndpoint = "https://api.newrelic.com/graphql"
	newRelicEUEndpoint = "https://api.eu.newrelic.com/graphql"
)

// newRelicQuery is the NerdGraph query. The account and the NRQL go in as
// variables, so the NRQL text is never spliced into GraphQL.
const newRelicQuery = `query($accountId: Int!, $nrql: Nrql!) { actor { account(id: $accountId) { nrql(query: $nrql) { results } } } }`

// NewRelicProvider runs NRQL through New Relic's NerdGraph API (POST, API-Key
// header). The query must return one row; the value is the row's
// resultField, or its only field.
type NewRelicProvider struct {
	// HTTPClient is used for the API calls; nil means the egress-guarded
	// default client.
	HTTPClient *http.Client
}

// newRelicResponse is the part of the NerdGraph response that is read.
type newRelicResponse struct {
	Data struct {
		Actor struct {
			Account *struct {
				NRQL *struct {
					Results []map[string]interface{} `json:"results"`
				} `json:"nrql"`
			} `json:"account"`
		} `json:"actor"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Evaluate implements Backend.
func (p *NewRelicProvider) Evaluate(ctx context.Context, q Query) (Value, error) {
	nr := q.Spec.NewRelic
	if nr == nil {
		return Value{}, errors.New("newrelic: spec.newRelic is required")
	}
	address := nr.Address
	if address == "" {
		address = newRelicUSEndpoint
		if nr.Region == "EU" {
			address = newRelicEUEndpoint
		}
	}
	endpoint, err := baseURL(address, "newrelic address")
	if err != nil {
		return Value{}, err
	}
	apiKey, err := q.Secret(ctx, nr.APIKeySecretRef)
	if err != nil {
		return Value{}, fmt.Errorf("newrelic API key: %w", err)
	}
	payload, err := json.Marshal(map[string]interface{}{
		"query":     newRelicQuery,
		"variables": map[string]interface{}{"accountId": nr.AccountID, "nrql": q.Spec.Query},
	})
	if err != nil {
		return Value{}, fmt.Errorf("encode newrelic request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return Value{}, errors.New("build newrelic request: invalid URL")
	}
	req.Header.Set("API-Key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	status, body, err := do(p.HTTPClient, req, "newrelic")
	if err != nil {
		return Value{}, err
	}
	var resp newRelicResponse
	decodeErr := json.Unmarshal(body, &resp)
	if status != http.StatusOK {
		if decodeErr == nil && len(resp.Errors) > 0 {
			return Value{}, fmt.Errorf("newrelic returned HTTP %d: %s", status, truncate(resp.Errors[0].Message))
		}
		return Value{}, fmt.Errorf("newrelic returned HTTP %d", status)
	}
	if decodeErr != nil {
		return Value{}, errors.New("decode newrelic response: not a NerdGraph response")
	}
	if len(resp.Errors) > 0 {
		msgs := make([]string, 0, len(resp.Errors))
		for _, e := range resp.Errors {
			msgs = append(msgs, e.Message)
		}
		return Value{}, fmt.Errorf("newrelic query error: %s", truncate(strings.Join(msgs, "; ")))
	}
	if resp.Data.Actor.Account == nil || resp.Data.Actor.Account.NRQL == nil {
		return Value{}, errors.New("newrelic query returned no result")
	}
	return newRelicValue(resp.Data.Actor.Account.NRQL.Results, nr.ResultField)
}

// newRelicValue reads field (or the only field) of the single result row.
func newRelicValue(rows []map[string]interface{}, field string) (Value, error) {
	switch n := len(rows); {
	case n == 0:
		return Value{}, errors.New("newrelic query returned no rows")
	case n > 1:
		return Value{}, fmt.Errorf("newrelic query returned %d rows, expected 1 (no FACET or TIMESERIES)", n)
	}
	row := rows[0]
	if field == "" {
		if len(row) != 1 {
			keys := make([]string, 0, len(row))
			for k := range row {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return Value{}, fmt.Errorf("newrelic result has fields %s: set newRelic.resultField", truncate(strings.Join(keys, ", ")))
		}
		for k := range row {
			field = k
		}
	}
	raw, ok := row[field]
	if !ok {
		return Value{}, fmt.Errorf("newrelic result has no field %q", field)
	}
	v, ok := jsonNumber(raw)
	if !ok {
		return Value{}, fmt.Errorf("newrelic result field %q is not a number", field)
	}
	return NumberValue(v), nil
}
