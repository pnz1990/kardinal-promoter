// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// MonitoringNamespace is where hack/e2e/components/prometheus.sh runs
// Prometheus and the Pushgateway.
const MonitoringNamespace = "monitoring"

// EnvPrometheusURL is the in-cluster Prometheus URL MetricChecks query.
const EnvPrometheusURL = "KARDINAL_E2E_PROMETHEUS_URL"

// PushedMetric is the metric PushMetric sets. Query it with a job label:
// PushedMetric+`{job="<job>"}`.
const PushedMetric = "kardinal_e2e_value"

// Service proxy paths through the API server, so tests reach in-cluster
// Services without port-forwards.
const (
	prometheusProxy  = "/api/v1/namespaces/" + MonitoringNamespace + "/services/http:prometheus:9090/proxy"
	pushgatewayProxy = "/api/v1/namespaces/" + MonitoringNamespace + "/services/http:pushgateway:9091/proxy"
	controllerProxy  = "/api/v1/namespaces/" + ControllerNamespace + "/services/http:kardinal-promoter:metrics/proxy"
)

// PrometheusURL is the Prometheus URL for MetricCheck.spec.prometheusURL.
func PrometheusURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv(EnvPrometheusURL)
	if u == "" {
		t.Fatalf("%s is not set; run hack/e2e/up.sh flux", EnvPrometheusURL)
	}
	return u
}

// PushMetric sets PushedMetric{job=job} to value in the Pushgateway, which
// Prometheus scrapes every 5s, and deletes the job's metrics when the test
// ends. Use a job name unique to the test, such as its namespace.
func (e *Env) PushMetric(t *testing.T, job string, value float64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body := fmt.Sprintf("# TYPE %s gauge\n%s %s\n", PushedMetric, PushedMetric, strconv.FormatFloat(value, 'g', -1, 64))
	err := e.Kube.CoreV1().RESTClient().Put().AbsPath(pushgatewayProxy, "metrics", "job", job).
		SetHeader("Content-Type", "text/plain; version=0.0.4").Body([]byte(body)).Do(ctx).Error()
	if err != nil {
		t.Fatalf("push %s{job=%q}=%g: %v", PushedMetric, job, value, err)
	}
	t.Cleanup(func() {
		if os.Getenv(EnvKeep) == "1" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = e.Kube.CoreV1().RESTClient().Delete().AbsPath(pushgatewayProxy, "metrics", "job", job).Do(ctx).Error()
	})
}

// SetMetric pushes value and waits until Prometheus returns it, so a
// MetricCheck evaluated afterwards sees it.
func (e *Env) SetMetric(t *testing.T, job string, value float64) {
	t.Helper()
	e.PushMetric(t, job, value)
	want := strconv.FormatFloat(value, 'g', -1, 64)
	q := PushedQuery(job)
	Eventually(t, time.Minute, "Prometheus to return "+q+" = "+want, func(ctx context.Context) (bool, string) {
		s, err := e.PromQuery(ctx, q)
		if err != nil {
			return false, err.Error()
		}
		return len(s) == 1 && s[0].Value == want, fmt.Sprintf("%v", s)
	})
}

// PushedQuery is the PromQL for the value PushMetric sets for job.
func PushedQuery(job string) string { return fmt.Sprintf("%s{job=%q}", PushedMetric, job) }

// PromSample is one series of a Prometheus instant query. A scalar result is
// one sample with no labels.
type PromSample struct {
	Metric map[string]string
	Value  string
}

func (s PromSample) String() string { return fmt.Sprintf("%v=%s", s.Metric, s.Value) }

// promResponse is the Prometheus HTTP API envelope.
type promResponse struct {
	Status    string          `json:"status"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
}

func (e *Env) promGet(ctx context.Context, path string, params map[string]string) (json.RawMessage, error) {
	req := e.Kube.CoreV1().RESTClient().Get().AbsPath(prometheusProxy, path)
	for k, v := range params {
		req = req.Param(k, v)
	}
	raw, err := req.DoRaw(ctx)
	var r promResponse
	if jerr := json.Unmarshal(raw, &r); jerr != nil {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("decode %s: %w", path, jerr)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("prometheus %s: %s: %s", path, r.ErrorType, r.Error)
	}
	return r.Data, nil
}

// PromQuery runs an instant query and returns its samples.
func (e *Env) PromQuery(ctx context.Context, query string) ([]PromSample, error) {
	data, err := e.promGet(ctx, "api/v1/query", map[string]string{"query": query})
	if err != nil {
		return nil, err
	}
	var d struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	switch d.ResultType {
	case "vector":
		var v []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]interface{}    `json:"value"`
		}
		if err := json.Unmarshal(d.Result, &v); err != nil {
			return nil, err
		}
		out := make([]PromSample, 0, len(v))
		for _, s := range v {
			val, _ := s.Value[1].(string)
			out = append(out, PromSample{Metric: s.Metric, Value: val})
		}
		return out, nil
	case "scalar":
		var v [2]interface{}
		if err := json.Unmarshal(d.Result, &v); err != nil {
			return nil, err
		}
		val, _ := v[1].(string)
		return []PromSample{{Value: val}}, nil
	default:
		return nil, fmt.Errorf("query %q: unsupported resultType %q", query, d.ResultType)
	}
}

// PromRuleGroup is one rule group from Prometheus's /api/v1/rules.
type PromRuleGroup struct {
	Name  string     `json:"name"`
	File  string     `json:"file"`
	Rules []PromRule `json:"rules"`
}

// PromRule is one recording or alerting rule. State is inactive, pending or
// firing for alerting rules.
type PromRule struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Query     string `json:"query"`
	Health    string `json:"health"`
	LastError string `json:"lastError"`
	State     string `json:"state"`
}

// PromRules returns the rule groups Prometheus loaded.
func (e *Env) PromRules(ctx context.Context) ([]PromRuleGroup, error) {
	data, err := e.promGet(ctx, "api/v1/rules", nil)
	if err != nil {
		return nil, err
	}
	var d struct {
		Groups []PromRuleGroup `json:"groups"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	return d.Groups, nil
}

// MetricSample is one sample of a Prometheus text exposition.
type MetricSample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// Metrics is a parsed /metrics page.
type Metrics []MetricSample

// Sum adds the values of every sample named name whose labels include
// labels.
func (m Metrics) Sum(name string, labels map[string]string) float64 {
	var total float64
	for _, s := range m {
		if s.Name == name && hasLabels(s.Labels, labels) {
			total += s.Value
		}
	}
	return total
}

// Has reports whether a sample named name with labels exists.
func (m Metrics) Has(name string, labels map[string]string) bool {
	for _, s := range m {
		if s.Name == name && hasLabels(s.Labels, labels) {
			return true
		}
	}
	return false
}

// LabelValues lists the distinct values of label across samples named name.
func (m Metrics) LabelValues(name, label string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range m {
		if v, ok := s.Labels[label]; ok && s.Name == name && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func hasLabels(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// ControllerMetrics fetches and parses the controller's /metrics through its
// Service.
func (e *Env) ControllerMetrics(ctx context.Context) (Metrics, error) {
	raw, err := e.Kube.CoreV1().RESTClient().Get().AbsPath(controllerProxy, "metrics").DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("get controller metrics: %w", err)
	}
	return ParseMetrics(string(raw))
}

// ParseMetrics parses the Prometheus text exposition format: comment lines
// are skipped, and each sample line is name{labels} value [timestamp].
func ParseMetrics(text string) (Metrics, error) {
	var out Metrics
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s, err := parseSample(line)
		if err != nil {
			return nil, fmt.Errorf("line %d %q: %w", i+1, line, err)
		}
		out = append(out, s)
	}
	return out, nil
}

func parseSample(line string) (MetricSample, error) {
	s := MetricSample{Labels: map[string]string{}}
	end := strings.IndexAny(line, "{ ")
	if end <= 0 {
		return s, fmt.Errorf("no metric name")
	}
	s.Name = line[:end]
	rest := line[end:]
	if strings.HasPrefix(rest, "{") {
		n, err := parseLabels(rest[1:], s.Labels)
		if err != nil {
			return s, err
		}
		rest = rest[1+n:]
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return s, fmt.Errorf("no value")
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return s, fmt.Errorf("value: %w", err)
	}
	s.Value = v
	return s, nil
}

// parseLabels reads name="value",... up to and including the closing brace
// and returns how many bytes it consumed.
func parseLabels(in string, labels map[string]string) (int, error) {
	i := 0
	for {
		for i < len(in) && (in[i] == ',' || in[i] == ' ') {
			i++
		}
		if i < len(in) && in[i] == '}' {
			return i + 1, nil
		}
		eq := strings.IndexByte(in[i:], '=')
		if eq < 0 || i+eq+1 >= len(in) || in[i+eq+1] != '"' {
			return 0, fmt.Errorf("malformed labels")
		}
		name := in[i : i+eq]
		i += eq + 2
		var b strings.Builder
		for ; i < len(in) && in[i] != '"'; i++ {
			if in[i] == '\\' && i+1 < len(in) {
				i++
				switch in[i] {
				case 'n':
					b.WriteByte('\n')
				default:
					b.WriteByte(in[i])
				}
				continue
			}
			b.WriteByte(in[i])
		}
		if i >= len(in) {
			return 0, fmt.Errorf("unterminated label value")
		}
		labels[name] = b.String()
		i++
	}
}
