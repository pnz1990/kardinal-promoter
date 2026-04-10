// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGraphNodePropagateWhenRoundtrip verifies that the PropagateWhen field
// marshals and unmarshals correctly, and that the json tag is "propagateWhen".
func TestGraphNodePropagateWhenRoundtrip(t *testing.T) {
	node := GraphNode{
		ID:            "no-weekend-deploys",
		PropagateWhen: []string{`${noWeekendDeploys.status.ready == true}`},
	}
	data, err := json.Marshal(node)
	require.NoError(t, err)
	assert.Contains(t, string(data), "propagateWhen",
		"marshaled JSON must contain the propagateWhen key")

	var got GraphNode
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, node.PropagateWhen, got.PropagateWhen,
		"PropagateWhen must round-trip without loss")
}

// TestGraphNodePropagateWhenOmitEmpty verifies that PropagateWhen is omitted
// from JSON when nil, so it does not pollute Graph specs for nodes that have
// no data-flow gate.
func TestGraphNodePropagateWhenOmitEmpty(t *testing.T) {
	node := GraphNode{
		ID:        "dev",
		ReadyWhen: []string{`${dev.status.phase == "Succeeded"}`},
	}
	data, err := json.Marshal(node)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "propagateWhen",
		"propagateWhen must be omitted from JSON when nil")
}

// TestGraphNodeAPIGroup verifies that GraphGVK uses the experimental.kro.run
// API group (updated in krocodile commit 48224264 on 2026-04-10).
func TestGraphNodeAPIGroup(t *testing.T) {
	assert.Equal(t, "experimental.kro.run", GraphGVK.Group,
		"GraphGVK.Group must be experimental.kro.run (krocodile commit 48224264)")
	assert.Equal(t, "v1alpha1", GraphGVK.Version)
	assert.Equal(t, "Graph", GraphGVK.Kind)
}

// TestGraphNodeDeepCopyPropagateWhen verifies that DeepCopyInto correctly
// copies the PropagateWhen slice without aliasing the original.
func TestGraphNodeDeepCopyPropagateWhen(t *testing.T) {
	original := GraphNode{
		ID:            "gate",
		PropagateWhen: []string{"expr1", "expr2"},
	}
	var copy GraphNode
	original.DeepCopyInto(&copy)
	assert.Equal(t, original.PropagateWhen, copy.PropagateWhen)

	// Mutate original; copy must not be affected
	original.PropagateWhen[0] = "mutated"
	assert.Equal(t, "expr1", copy.PropagateWhen[0],
		"DeepCopyInto must not alias PropagateWhen slice")
}

// TestGraphSpecDeepCopy verifies GraphSpec.DeepCopy handles nil and non-nil.
func TestGraphSpecDeepCopy(t *testing.T) {
	// Nil receiver
	var nilSpec *GraphSpec
	assert.Nil(t, nilSpec.DeepCopy())

	// Non-nil with nodes
	original := &GraphSpec{
		Nodes: []GraphNode{
			{ID: "node1", PropagateWhen: []string{"expr1"}},
		},
	}
	copied := original.DeepCopy()
	require.NotNil(t, copied)
	assert.Equal(t, original.Nodes[0].ID, copied.Nodes[0].ID)

	// Mutate original — copy must not be affected
	original.Nodes[0].PropagateWhen[0] = "mutated"
	assert.Equal(t, "expr1", copied.Nodes[0].PropagateWhen[0],
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
		ForEach:     "forEach",
		Template:    map[string]interface{}{"key": "value"},
	}
	copied := original.DeepCopy()
	require.NotNil(t, copied)
	assert.Equal(t, original.ID, copied.ID)
	assert.Equal(t, original.ForEach, copied.ForEach)
	assert.Equal(t, original.ReadyWhen, copied.ReadyWhen)
	assert.Equal(t, original.IncludeWhen, copied.IncludeWhen)
	assert.Equal(t, original.Template["key"], copied.Template["key"])
}

// TestGraphSpecDeepCopyIntoEmptyNodes verifies DeepCopyInto with nil Nodes.
func TestGraphSpecDeepCopyIntoEmptyNodes(t *testing.T) {
	original := &GraphSpec{}
	var out GraphSpec
	original.DeepCopyInto(&out)
	assert.Nil(t, out.Nodes)
}
