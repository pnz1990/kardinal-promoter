// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flowSchemas returns the FlowSchemas the chart renders.
func flowSchemas(t *testing.T, args ...string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, d := range renderChart(t, "kp", append([]string{"--namespace", "kardinal-system"}, args...)...) {
		if d["kind"] == "FlowSchema" {
			out = append(out, d)
		}
	}
	return out
}

// TestChartLeaderElectionFlowSchema (#1592): on a cluster with the
// flowcontrol v1 API the chart sends the controller ServiceAccount's Lease
// requests in its namespace to the leader-election priority level, ahead of
// any FlowSchema for its other requests; off when disabled or without
// leader election. (The template also skips it without the API, which helm
// template cannot simulate: its default capabilities include it.)
//
// Covers CHART-LEASE-APF-01.
func TestChartLeaderElectionFlowSchema(t *testing.T) {
	api := "--api-versions=flowcontrol.apiserver.k8s.io/v1"
	fs := flowSchemas(t, api)
	require.Len(t, fs, 1)
	assert.Equal(t, "kp-kardinal-promoter-kardinal-system-leader-election", dig(fs[0], "metadata", "name"))
	assert.EqualValues(t, 90, dig(fs[0], "spec", "matchingPrecedence"))
	assert.Equal(t, "leader-election", dig(fs[0], "spec", "priorityLevelConfiguration", "name"))
	rule := dig(fs[0], "spec", "rules").([]interface{})[0]
	subject := dig(rule, "subjects").([]interface{})[0]
	assert.Equal(t, "kardinal-system", dig(subject, "serviceAccount", "namespace"))
	assert.NotEmpty(t, dig(subject, "serviceAccount", "name"))
	res := dig(rule, "resourceRules").([]interface{})[0]
	assert.Equal(t, []interface{}{"coordination.k8s.io"}, dig(res, "apiGroups"))
	assert.Equal(t, []interface{}{"leases"}, dig(res, "resources"))
	assert.Equal(t, []interface{}{"kardinal-system"}, dig(res, "namespaces"))

	assert.Empty(t, flowSchemas(t, api, "--set", "leaderElectionFlowSchema.enabled=false"))
	assert.Empty(t, flowSchemas(t, api, "--set", "leaderElect=false"))
	fs = flowSchemas(t, api, "--set", "leaderElectionFlowSchema.matchingPrecedence=50")
	require.Len(t, fs, 1)
	assert.EqualValues(t, 50, dig(fs[0], "spec", "matchingPrecedence"))
}
