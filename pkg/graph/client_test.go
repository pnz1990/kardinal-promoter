// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
)

func newTestGraph(nodeIDs ...string) *Graph {
	g := &Graph{
		TypeMeta: metav1.TypeMeta{APIVersion: "kro.run/v1alpha1", Kind: "Graph"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-app-v1", Namespace: "default",
			Labels: map[string]string{"kardinal.io/bundle": "app-v1"},
		},
	}
	for _, id := range nodeIDs {
		g.Spec.Nodes = append(g.Spec.Nodes, GraphNode{ID: id, Template: map[string]interface{}{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]interface{}{"name": id},
		}})
	}
	return g
}

// TestGraphClient_CreateUpdatesInPlace verifies that Create on an existing Graph
// updates its spec instead of skipping it or recreating it, keeps labels set by
// others, and does not write when nothing changed (ledger G6).
func TestGraphClient_CreateUpdatesInPlace(t *testing.T) {
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{GraphGVR: "GraphList"})
	c := NewGraphClient(dyn, zerolog.Nop())
	ctx := context.Background()

	require.NoError(t, c.Create(ctx, newTestGraph("test")))

	// Another controller labels the Graph.
	u, err := dyn.Resource(GraphGVR).Namespace("default").Get(ctx, "app-app-v1", metav1.GetOptions{})
	require.NoError(t, err)
	labels := u.GetLabels()
	labels["example.com/team"] = "payments"
	u.SetLabels(labels)
	_, err = dyn.Resource(GraphGVR).Namespace("default").Update(ctx, u, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.NoError(t, c.Create(ctx, newTestGraph("test", "prod")))
	got, err := c.Get(ctx, "default", "app-app-v1")
	require.NoError(t, err)
	require.Len(t, got.Spec.Nodes, 2, "spec must be updated in place")
	assert.Equal(t, "prod", got.Spec.Nodes[1].ID)
	assert.Equal(t, "payments", got.Labels["example.com/team"], "foreign labels must survive")

	dyn.ClearActions()
	require.NoError(t, c.Create(ctx, newTestGraph("test", "prod")))
	for _, a := range dyn.Actions() {
		assert.NotContains(t, []string{"update", "delete"}, a.GetVerb(),
			"unchanged Graph must not be written")
	}
}
