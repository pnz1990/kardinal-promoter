// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// EnvMetricsAPIURL is the in-cluster base URL of the fake metrics APIs
// (hack/e2e/metricsapi, hack/e2e/components/metrics-api.sh).
const EnvMetricsAPIURL = "KARDINAL_E2E_METRICSAPI_URL"

// metricsAPIProxy reaches the fake metrics APIs through the API server.
const metricsAPIProxy = "/api/v1/namespaces/metrics-api/services/http:metrics-api:8080/proxy"

// Credentials the fake metrics APIs accept (their flag defaults).
const (
	FakeDatadogAPIKey    = "e2e-dd-api-key"
	FakeDatadogAppKey    = "e2e-dd-app-key"
	FakeNewRelicAPIKey   = "e2e-nr-api-key"
	FakeAWSAccessKeyID   = "AKIDE2EKARDINAL"
	FakeAWSSecretKey     = "e2e-aws-secret"
	FakeWebAuthorization = "Bearer e2e-web-token"
)

// MetricsAPIURL is the fake metrics APIs' in-cluster base URL.
func MetricsAPIURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv(EnvMetricsAPIURL)
	if u == "" {
		t.Fatalf("%s is not set; run hack/e2e/up.sh flux", EnvMetricsAPIURL)
	}
	return u
}

// FakeMetricRecord is one request a fake metrics API got.
type FakeMetricRecord struct {
	Time   time.Time         `json:"time"`
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  string            `json:"query"`
	Auth   string            `json:"auth"`
	Status int               `json:"status"`
	Header map[string]string `json:"header"`
	Body   string            `json:"body"`
}

// SetFakeMetric makes the fake provider (datadog, newrelic, cloudwatch)
// return value for query; nil makes the query return no data. The value is
// removed when the test ends.
func (e *Env) SetFakeMetric(t *testing.T, provider, query string, value *float64) {
	t.Helper()
	put := func(v *float64) error {
		body, err := json.Marshal(map[string]interface{}{"query": query, "value": v})
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return e.Kube.CoreV1().RESTClient().Put().AbsPath(metricsAPIProxy, "_values", provider).
			SetHeader("Content-Type", "application/json").Body(body).Do(ctx).Error()
	}
	if err := put(value); err != nil {
		t.Fatalf("set fake %s value of %q: %v", provider, query, err)
	}
	t.Cleanup(func() { _ = put(nil) })
}

// SetFakeWebDoc makes the fake web API answer doc at /web/<path>.
func (e *Env) SetFakeWebDoc(t *testing.T, path string, doc interface{}) {
	t.Helper()
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.Kube.CoreV1().RESTClient().Put().AbsPath(metricsAPIProxy, "_web", path).
		SetHeader("Content-Type", "application/json").Body(body).Do(ctx).Error(); err != nil {
		t.Fatalf("set fake web document %s: %v", path, err)
	}
}

// FakeMetricRecords returns the requests the fake provider got for query,
// oldest first.
func (e *Env) FakeMetricRecords(ctx context.Context, provider, query string) ([]FakeMetricRecord, error) {
	raw, err := e.Kube.CoreV1().RESTClient().Get().AbsPath(metricsAPIProxy, "_records", provider).DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("get fake %s records: %w", provider, err)
	}
	var all []FakeMetricRecord
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("decode fake %s records: %w", provider, err)
	}
	var out []FakeMetricRecord
	for _, r := range all {
		if r.Query == query {
			out = append(out, r)
		}
	}
	return out, nil
}

// LabelReferenceable must be "true" on a Secret that a MetricCheck,
// NotificationHook or Subscription may name.
const LabelReferenceable = "kardinal.io/referenceable"

// CreateSecretData creates the Secret ns/name with data, labelled
// kardinal.io/referenceable: "true" when referenceable.
func (e *Env) CreateSecretData(t *testing.T, ns, name string, referenceable bool, data map[string]string) {
	t.Helper()
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, StringData: data}
	if referenceable {
		s.Labels = map[string]string{LabelReferenceable: "true"}
	}
	if err := e.Client.Create(context.Background(), s); err != nil {
		t.Fatalf("create Secret %s/%s: %v", ns, name, err)
	}
}

// UpdateSecretData replaces the data of the Secret ns/name.
func (e *Env) UpdateSecretData(t *testing.T, ns, name string, data map[string]string) {
	t.Helper()
	var s corev1.Secret
	if err := e.Client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &s); err != nil {
		t.Fatalf("get Secret %s/%s: %v", ns, name, err)
	}
	s.Data, s.StringData = nil, data
	if err := e.Client.Update(context.Background(), &s); err != nil {
		t.Fatalf("update Secret %s/%s: %v", ns, name, err)
	}
}

// ListMetricChecks lists the MetricChecks in ns with labels.
func (e *Env) ListMetricChecks(ctx context.Context, ns string, labels map[string]string) ([]v1alpha1.MetricCheck, error) {
	var list v1alpha1.MetricCheckList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels(labels)); err != nil {
		return nil, err
	}
	return list.Items, nil
}
