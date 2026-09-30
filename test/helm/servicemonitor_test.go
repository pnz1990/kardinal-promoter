// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findKind returns the rendered documents of one kind.
func findKind(docs []map[string]interface{}, kind string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, d := range docs {
		if d["kind"] == kind {
			out = append(out, d)
		}
	}
	return out
}

func TestHelmTemplateServiceMonitorDisabledByDefault(t *testing.T) {
	docs := renderChart(t, "kardinal-promoter")
	assert.Empty(t, findKind(docs, "ServiceMonitor"),
		"ServiceMonitor must not be rendered by default: its CRD exists only with Prometheus Operator")
}

// TestHelmTemplateServiceMonitorScrapesMetricsService checks that the
// ServiceMonitor scrapes the chart Service's metrics port over plain HTTP.
// Prometheus Operator sets the job label to the Service name, so the Service
// must be named fullname for the PrometheusRule alerts (job="<fullname>") to match.
func TestHelmTemplateServiceMonitorScrapesMetricsService(t *testing.T) {
	const release = "kp"
	docs := renderChart(t, release,
		"--namespace", "kardinal-system",
		"--set", "serviceMonitor.enabled=true",
		"--set", "serviceMonitor.interval=30s",
		"--set", "serviceMonitor.labels.release=kube-prometheus-stack",
		"--set", "prometheusRule.enabled=true")

	sms := findKind(docs, "ServiceMonitor")
	require.Len(t, sms, 1)
	sm := sms[0]
	assert.Equal(t, "monitoring.coreos.com/v1", sm["apiVersion"])
	assert.Equal(t, "kardinal-system", dig(sm, "metadata", "namespace"))
	assert.Equal(t, "kube-prometheus-stack", dig(sm, "metadata", "labels", "release"),
		"serviceMonitor.labels must reach the ServiceMonitor metadata")
	assert.Equal(t, []interface{}{"kardinal-system"}, dig(sm, "spec", "namespaceSelector", "matchNames"))

	endpoints, _ := dig(sm, "spec", "endpoints").([]interface{})
	require.Len(t, endpoints, 1)
	assert.Equal(t, "metrics", dig(endpoints[0], "port"))
	assert.Equal(t, "http", dig(endpoints[0], "scheme"), "the metrics server serves plain HTTP")
	assert.Equal(t, "/metrics", dig(endpoints[0], "path"))
	assert.Equal(t, "30s", dig(endpoints[0], "interval"))

	// The selector must pick the chart Service, and that Service must expose
	// a port named metrics.
	selector, _ := dig(sm, "spec", "selector", "matchLabels").(map[string]interface{})
	require.NotEmpty(t, selector)
	var matched []map[string]interface{}
	for _, svc := range findKind(docs, "Service") {
		labels, _ := dig(svc, "metadata", "labels").(map[string]interface{})
		all := true
		for k, v := range selector {
			if labels[k] != v {
				all = false
			}
		}
		if all {
			matched = append(matched, svc)
		}
	}
	require.Len(t, matched, 1, "the selector must match exactly the controller Service")
	svc := matched[0]
	ports, _ := dig(svc, "spec", "ports").([]interface{})
	hasMetrics := false
	for _, p := range ports {
		if dig(p, "name") == "metrics" {
			hasMetrics = true
		}
	}
	assert.True(t, hasMetrics, "the selected Service must expose a port named metrics")

	// The job label (the Service name) is what the alerts select on.
	svcName, _ := dig(svc, "metadata", "name").(string)
	require.NotEmpty(t, svcName)
	rules := findKind(docs, "PrometheusRule")
	require.Len(t, rules, 1)
	groups, _ := dig(rules[0], "spec", "groups").([]interface{})
	found := false
	for _, g := range groups {
		rs, _ := dig(g, "rules").([]interface{})
		for _, r := range rs {
			if dig(r, "alert") == "KardinalControllerDown" {
				expr, _ := dig(r, "expr").(string)
				assert.Contains(t, expr, `job="`+svcName+`"`,
					"the alerts must select the job label the ServiceMonitor produces")
				found = true
			}
		}
	}
	assert.True(t, found, "KardinalControllerDown alert must exist")
}

func TestHelmTemplateServiceMonitorNoIntervalByDefault(t *testing.T) {
	docs := renderChart(t, "kardinal-promoter", "--set", "serviceMonitor.enabled=true")
	sms := findKind(docs, "ServiceMonitor")
	require.Len(t, sms, 1)
	endpoints, _ := dig(sms[0], "spec", "endpoints").([]interface{})
	require.Len(t, endpoints, 1)
	assert.Nil(t, dig(endpoints[0], "interval"), "an empty interval uses the Prometheus default")
}
