// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMetrics(t *testing.T) {
	m, err := ParseMetrics(`# HELP kardinal_bundles_total Bundles by phase.
# TYPE kardinal_bundles_total counter
kardinal_bundles_total{phase="Verified"} 3
kardinal_bundles_total{phase="Failed"} 1 1700000000000

go_goroutines 42
odd{msg="a \"quoted\" \\ value\nline two",x="1",} 2.5e+01
`)
	require.NoError(t, err)
	require.Len(t, m, 4)
	assert.Equal(t, MetricSample{Name: "kardinal_bundles_total", Labels: map[string]string{"phase": "Failed"}, Value: 1}, m[1])
	assert.Equal(t, MetricSample{Name: "go_goroutines", Labels: map[string]string{}, Value: 42}, m[2])
	assert.Equal(t, map[string]string{"msg": "a \"quoted\" \\ value\nline two", "x": "1"}, m[3].Labels)
	assert.Equal(t, 25.0, m[3].Value)

	for _, bad := range []string{
		`{phase="x"} 1`,
		`name{phase="x"}`,
		`name{phase=x} 1`,
		`name{phase="x 1`,
		`name not-a-number`,
	} {
		_, err := ParseMetrics(bad)
		assert.Error(t, err, bad)
	}
}

func TestMetricsQueries(t *testing.T) {
	m := Metrics{
		{Name: "reconcile_total", Labels: map[string]string{"controller": "bundle", "result": "success"}, Value: 3},
		{Name: "reconcile_total", Labels: map[string]string{"controller": "bundle", "result": "error"}, Value: 1},
		{Name: "reconcile_total", Labels: map[string]string{"controller": "policygate", "result": "success"}, Value: 5},
		{Name: "other", Labels: map[string]string{"controller": "bundle"}, Value: 100},
	}
	assert.Equal(t, 9.0, m.Sum("reconcile_total", nil))
	assert.Equal(t, 4.0, m.Sum("reconcile_total", map[string]string{"controller": "bundle"}))
	assert.Equal(t, 1.0, m.Sum("reconcile_total", map[string]string{"controller": "bundle", "result": "error"}))
	assert.Equal(t, 0.0, m.Sum("reconcile_total", map[string]string{"controller": "pipeline"}))
	assert.True(t, m.Has("other", map[string]string{"controller": "bundle"}))
	assert.False(t, m.Has("other", map[string]string{"controller": "policygate"}))
	assert.Equal(t, []string{"bundle", "policygate"}, m.LabelValues("reconcile_total", "controller"))
	assert.Equal(t, []string{"error", "success"}, m.LabelValues("reconcile_total", "result"))
	assert.Empty(t, m.LabelValues("reconcile_total", "missing"))
}
