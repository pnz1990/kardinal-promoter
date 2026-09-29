// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGraphNodeForEachRoundtrip verifies that ForEach marshals as kro's
// []ForEachDimension shape: a list of single-entry {iterator: expression} maps.
func TestGraphNodeForEachRoundtrip(t *testing.T) {
	node := GraphNode{
		ID:      "prod",
		ForEach: []map[string]string{{"region": `${["us-east-1","eu-west-1"]}`}},
	}
	data, err := json.Marshal(node)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"forEach":[{"region":`)

	var got GraphNode
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, node.ForEach, got.ForEach, "ForEach must round-trip without loss")
}

// TestGraphNodeOmitEmpty verifies optional fields are omitted when unset, so
// Graph specs only carry the keywords kro's schema defines.
func TestGraphNodeOmitEmpty(t *testing.T) {
	node := GraphNode{
		ID:        "dev",
		ReadyWhen: []string{`${dev.status.state == "Verified"}`},
	}
	data, err := json.Marshal(node)
	require.NoError(t, err)
	for _, key := range []string{"forEach", "includeWhen", "template", "ref"} {
		assert.NotContains(t, string(data), key, "%s must be omitted when unset", key)
	}
}

// TestGraphNodeAPIGroup verifies that GraphGVK/GraphGVR point at the upstream
// kro Graph (kro.run/v1alpha1, kro v0.10.0+).
func TestGraphNodeAPIGroup(t *testing.T) {
	assert.Equal(t, "kro.run", GraphGVK.Group)
	assert.Equal(t, "v1alpha1", GraphGVK.Version)
	assert.Equal(t, "Graph", GraphGVK.Kind)
	assert.Equal(t, "kro.run", GraphGVR.Group)
	assert.Equal(t, "graphs", GraphGVR.Resource)
}

// TestGraphSpecServiceAccountJSON verifies the serviceAccountName json tag.
func TestGraphSpecServiceAccountJSON(t *testing.T) {
	data, err := json.Marshal(GraphSpec{ServiceAccountName: "kardinal-graph"})
	require.NoError(t, err)
	assert.Contains(t, string(data), `"serviceAccountName":"kardinal-graph"`)
}
