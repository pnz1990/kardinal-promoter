// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
)

// TestFakeAgreesWithProviders runs kardinal's real providers against the
// fake, so the live tests exercise the same wire format: right credentials
// give the value set for the query, wrong ones the service's auth error, and
// a query with no value no data.
func TestFakeAgreesWithProviders(t *testing.T) {
	c := creds{ddAPIKey: "dd", ddAppKey: "app", nrAPIKey: "nr", awsKeyID: "AKIDTEST", awsKey: "aws-secret", webAuth: "Bearer w"}
	s := newServer(c)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	hc := &http.Client{Timeout: 5 * time.Second}

	set := func(provider, query string, v float64) {
		req, err := http.NewRequest(http.MethodPut, srv.URL+"/_values/"+provider,
			bytes.NewBufferString(fmt.Sprintf(`{"query":%q,"value":%g}`, query, v)))
		require.NoError(t, err)
		resp, err := hc.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
		_ = resp.Body.Close()
	}
	set("datadog", "avg:err{v:1}", 0.5)
	set("newrelic", "SELECT 1", 2)
	set("cloudwatch", "SELECT AVG(x)", 3)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/_web/ns/check", bytes.NewBufferString(`{"status":"ok","n":4}`))
	resp, err := hc.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	good := map[string]string{"s/dd": "dd", "s/app": "app", "s/nr": "nr", "s/id": "AKIDTEST", "s/key": "aws-secret", "s/web": "Bearer w"}
	bad := map[string]string{"s/dd": "x", "s/app": "x", "s/nr": "x", "s/id": "AKIDTEST", "s/key": "wrong", "s/web": "x"}
	secret := func(m map[string]string) func(context.Context, kardinalv1alpha1.SecretKeyRef) (string, error) {
		return func(_ context.Context, r kardinalv1alpha1.SecretKeyRef) (string, error) {
			return m[r.Name+"/"+r.Key], nil
		}
	}
	ref := func(k string) kardinalv1alpha1.SecretKeyRef { return kardinalv1alpha1.SecretKeyRef{Name: "s", Key: k} }
	idRef, keyRef := ref("id"), ref("key")
	cases := []struct {
		name    string
		backend metriccheck.Backend
		spec    kardinalv1alpha1.MetricCheckSpec
		want    string
		badErr  string
	}{
		{"datadog", &metriccheck.DatadogProvider{HTTPClient: hc}, kardinalv1alpha1.MetricCheckSpec{Query: "avg:err{v:1}",
			Datadog: &kardinalv1alpha1.DatadogProviderSpec{Address: srv.URL + "/datadog", APIKeySecretRef: ref("dd"), ApplicationKeySecretRef: ref("app")}},
			"0.5", "HTTP 403: Forbidden"},
		{"newrelic", &metriccheck.NewRelicProvider{HTTPClient: hc}, kardinalv1alpha1.MetricCheckSpec{Query: "SELECT 1",
			NewRelic: &kardinalv1alpha1.NewRelicProviderSpec{AccountID: 1, Address: srv.URL + "/newrelic/graphql", APIKeySecretRef: ref("nr")}},
			"2", "HTTP 401: Invalid API key"},
		{"cloudwatch", &metriccheck.CloudWatchProvider{HTTPClient: hc}, kardinalv1alpha1.MetricCheckSpec{Query: "SELECT AVG(x)",
			CloudWatch: &kardinalv1alpha1.CloudWatchProviderSpec{Region: "eu-west-1", Endpoint: srv.URL + "/cloudwatch/",
				AccessKeyIDSecretRef: &idRef, SecretAccessKeySecretRef: &keyRef}},
			"3", "SignatureDoesNotMatch: signature does not match"},
		{"web", &metriccheck.WebProvider{HTTPClient: hc}, kardinalv1alpha1.MetricCheckSpec{Web: &kardinalv1alpha1.WebProviderSpec{
			URL: srv.URL + "/web/ns/check", JSONPath: "{.status}",
			Headers: []kardinalv1alpha1.WebHeader{{Name: "Authorization", ValueFromSecret: &kardinalv1alpha1.SecretKeyRef{Name: "s", Key: "web"}}}}},
			"ok", "HTTP 401"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := tc.backend.Evaluate(context.Background(), metriccheck.Query{Spec: &tc.spec, Secret: secret(good), Now: time.Now()})
			require.NoError(t, err)
			assert.Equal(t, tc.want, v.Text)
			_, err = tc.backend.Evaluate(context.Background(), metriccheck.Query{Spec: &tc.spec, Secret: secret(bad), Now: time.Now()})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.badErr)
		})
	}

	t.Run("no data", func(t *testing.T) {
		spec := cases[0].spec
		spec.Query = "avg:unset{*}"
		_, err := cases[0].backend.Evaluate(context.Background(), metriccheck.Query{Spec: &spec, Secret: secret(good), Now: time.Now()})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no series")
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Equal(t, "ok", s.records["cloudwatch"][0].Auth)
	assert.Equal(t, "signature does not match", s.records["cloudwatch"][1].Auth)
}
