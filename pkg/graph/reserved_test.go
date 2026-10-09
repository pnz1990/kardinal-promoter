// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestReservedEnvironmentNamesMatchCRD: kardinal validate reports a reserved
// environment name with the API server's list and message (#1358), so the
// two must not drift from the Pipeline CRD's rule.
func TestReservedEnvironmentNamesMatchCRD(t *testing.T) {
	src, err := os.ReadFile("../../api/v1alpha1/pipeline_types.go")
	require.NoError(t, err)
	m := regexp.MustCompile(`rule="!\(self\.name in \[([^\]]*)\]\)",message="([^"]*)"`).FindSubmatch(src)
	require.NotNil(t, m, "the reserved-name rule is on EnvironmentSpec")
	names := strings.Split(strings.ReplaceAll(string(m[1]), "'", ""), ",")
	for _, n := range names {
		assert.True(t, graph.ReservedEnvironmentName(n), "%q is reserved by the CRD", n)
	}
	assert.Len(t, names, 38)
	assert.Equal(t, string(m[2]), graph.ReservedEnvironmentMessage)
	assert.True(t, strings.HasSuffix(graph.ReservedEnvironmentMessage, "; rename the environment"))
	for _, ok := range []string{"prod", "bundles", "specs", "api"} {
		assert.False(t, graph.ReservedEnvironmentName(ok), ok)
	}
}
