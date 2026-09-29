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

// TestGraphNodeDeepCopyForEach verifies that DeepCopyInto copies the ForEach
// dimensions without aliasing the original maps.
func TestGraphNodeDeepCopyForEach(t *testing.T) {
	original := GraphNode{
		ID:      "prod",
		ForEach: []map[string]string{{"region": "${a}"}},
	}
	var copied GraphNode
	original.DeepCopyInto(&copied)
	assert.Equal(t, original.ForEach, copied.ForEach)

	original.ForEach[0]["region"] = "mutated"
	assert.Equal(t, "${a}", copied.ForEach[0]["region"],
		"DeepCopyInto must not alias ForEach maps")
}

// TestGraphSpecDeepCopy verifies GraphSpec.DeepCopy handles nil and non-nil.
func TestGraphSpecDeepCopy(t *testing.T) {
	// Nil receiver
	var nilSpec *GraphSpec
	assert.Nil(t, nilSpec.DeepCopy())

	// Non-nil with nodes
	original := &GraphSpec{
		ServiceAccountName: "sa",
		Nodes: []GraphNode{
			{ID: "node1", ReadyWhen: []string{"expr1"}},
		},
	}
	copied := original.DeepCopy()
	require.NotNil(t, copied)
	assert.Equal(t, original.Nodes[0].ID, copied.Nodes[0].ID)
	assert.Equal(t, "sa", copied.ServiceAccountName)

	// Mutate original — copy must not be affected
	original.Nodes[0].ReadyWhen[0] = "mutated"
	assert.Equal(t, "expr1", copied.Nodes[0].ReadyWhen[0],
		"DeepCopy must not alias slice contents")
}

// TestGraphNodeDeepCopy verifies GraphNode.DeepCopy nil safety.
func TestGraphNodeDeepCopy(t *testing.T) {
	var nilNode *GraphNode
	assert.Nil(t, nilNode.DeepCopy())

	original := &GraphNode{
		ID:          "gate",
		ReadyWhen:   []string{"r1"},
		IncludeWhen: []string{"i1"},
		ForEach:     []map[string]string{{"region": "${r}"}},
		Template:    map[string]interface{}{"key": "value"},
		Ref:         map[string]interface{}{"kind": "Bundle"},
	}
	copied := original.DeepCopy()
	require.NotNil(t, copied)
	assert.Equal(t, original.ID, copied.ID)
	assert.Equal(t, original.ForEach, copied.ForEach)
	assert.Equal(t, original.ReadyWhen, copied.ReadyWhen)
	assert.Equal(t, original.IncludeWhen, copied.IncludeWhen)
	assert.Equal(t, original.Template["key"], copied.Template["key"])
	assert.Equal(t, original.Ref["kind"], copied.Ref["kind"])
}

// TestGraphSpecDeepCopyIntoEmptyNodes verifies DeepCopyInto with nil Nodes.
func TestGraphSpecDeepCopyIntoEmptyNodes(t *testing.T) {
	original := &GraphSpec{}
	var out GraphSpec
	original.DeepCopyInto(&out)
	assert.Nil(t, out.Nodes)
}
