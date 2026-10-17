// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// oldGraph is a Graph as a release before #1283 built it: a PromotionStep,
// the Bundle ref, health ref nodes and the reader labels.
func oldGraph(ns, name string, health bool) *unstructured.Unstructured {
	nodes := []interface{}{
		map[string]interface{}{"id": "prod", "template": map[string]interface{}{"kind": "PromotionStep"}},
		map[string]interface{}{"id": "bundle", "ref": map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "Bundle"}},
		// An environment named "healthy" is not a health ref.
		map[string]interface{}{"id": "healthy", "template": map[string]interface{}{"kind": "PromotionStep"}},
	}
	labels := map[string]interface{}{"kardinal.io/bundle": name}
	if health {
		nodes = append(nodes,
			map[string]interface{}{"id": "healthProd", "ref": map[string]interface{}{"apiVersion": "apps/v1", "kind": "Deployment"}},
			map[string]interface{}{"id": "healthProd2", "ref": map[string]interface{}{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application"}},
		)
		labels["kardinal.io/reader-labels"] = "true"
		labels["reads.kardinal.io/argocd"] = "true"
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kro.run/v1alpha1", "kind": "Graph",
		"metadata": map[string]interface{}{"name": name, "namespace": ns, "labels": labels},
		"spec":     map[string]interface{}{"nodes": nodes},
	}}
}

func TestIsHealthRef(t *testing.T) {
	ref := func(id, kind string) graph.GraphNode {
		return graph.GraphNode{ID: id, Ref: map[string]interface{}{"kind": kind}}
	}
	assert.True(t, graph.IsHealthRef(ref("healthProd", "Deployment")))
	assert.True(t, graph.IsHealthRef(ref("healthProdEu2", "Kustomization")))
	assert.True(t, graph.IsHealthRef(ref("health1", "Canary")))
	assert.False(t, graph.IsHealthRef(ref("healthy", "Deployment")), "the next letter is lower case")
	assert.False(t, graph.IsHealthRef(ref("health", "Deployment")))
	assert.False(t, graph.IsHealthRef(ref("healthProd", "Bundle")), "not a health kind")
	assert.False(t, graph.IsHealthRef(graph.GraphNode{ID: "healthProd"}), "not a ref")
}

// #1283: the health ref nodes and reader labels are removed from the Graphs
// an earlier release built, page by page, every other node and label kept;
// a Graph without them is not written, and a second pass changes nothing.
func TestGraphClient_RemoveAllHealthRefs(t *testing.T) {
	ctx := context.Background()
	labelsOnly := oldGraph("team-c", "g4", false)
	labelsOnly.SetLabels(map[string]string{"kardinal.io/bundle": "g4", "kardinal.io/reader-labels": "true"})
	objs := []runtime.Object{oldGraph("team-a", "g1", true), oldGraph("team-b", "g2", true), oldGraph("team-a", "g3", false), labelsOnly}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{graph.GraphGVR: "GraphList"}, objs...)
	gc := graph.NewGraphClient(dyn, zerolog.Nop())

	n, failed, err := gc.RemoveAllHealthRefs(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Zero(t, failed)
	updates := 0
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "update" {
			updates++
		}
	}
	assert.Equal(t, 3, updates, "g3 has none and is not written")
	g4, err := gc.Get(ctx, "team-c", "g4")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"kardinal.io/bundle": "g4"}, g4.Labels, "a Graph with only the reader labels loses them")
	g, err := gc.Get(ctx, "team-a", "g1")
	require.NoError(t, err)
	var ids []string
	for _, node := range g.Spec.Nodes {
		ids = append(ids, node.ID)
	}
	assert.Equal(t, []string{"prod", "bundle", "healthy"}, ids)
	assert.Equal(t, map[string]string{"kardinal.io/bundle": "g1"}, g.Labels)
	assert.False(t, graph.HasHealthRefs(g))

	n, failed, err = gc.RemoveAllHealthRefs(ctx, "team-b")
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Zero(t, failed)
	changed, err := gc.RemoveHealthRefs(ctx, "team-a", "gone")
	require.NoError(t, err)
	assert.False(t, changed, "a Graph that is gone is no change")
}

// QA on #2124: a Graph whose write is refused, in the middle of the others,
// is skipped: the Graphs after it are cleaned, and it is reported as failed
// on every pass while the refusal lasts.
//
// Covers GRAPH-HEALTHREF-CLEANUP-02.
func TestGraphClient_RemoveAllHealthRefs_SkipsAFailingGraph(t *testing.T) {
	ctx := context.Background()
	var objs []runtime.Object
	for _, name := range []string{"g1", "g2", "g3", "g4"} {
		objs = append(objs, oldGraph("team-a", name, true))
	}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{graph.GraphGVR: "GraphList"}, objs...)
	dyn.PrependReactor("update", "graphs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured).GetName() == "g2" {
			return true, nil, apierrors.NewForbidden(graph.GraphGVR.GroupResource(), "g2", errors.New("denied"))
		}
		return false, nil, nil
	})
	gc := graph.NewGraphClient(dyn, zerolog.Nop())
	for pass := 0; pass < 2; pass++ {
		changed, failed, err := gc.RemoveAllHealthRefs(ctx, "team-a")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "team-a/g2")
		assert.Equal(t, 1, failed, "pass %d", pass)
		if pass == 0 {
			assert.Equal(t, 3, changed, "g1, g3 and g4")
		} else {
			assert.Zero(t, changed, "already clean")
		}
	}
	for _, name := range []string{"g1", "g3", "g4"} {
		g, err := gc.Get(ctx, "team-a", name)
		require.NoError(t, err)
		assert.False(t, graph.HasHealthRefs(g), name)
	}
	g2, err := gc.Get(ctx, "team-a", "g2")
	require.NoError(t, err)
	assert.True(t, graph.HasHealthRefs(g2), "the refused Graph is left as it was")
}
