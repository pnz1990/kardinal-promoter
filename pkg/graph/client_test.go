// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func newTestGraph(nodeIDs ...string) *Graph {
	return graphOwnedBy("app-v1", "uid-v1", nodeIDs...)
}

// graphOwnedBy returns the Graph "default/app-app-v1" controlled by the
// Bundle bundle with the given UID.
func graphOwnedBy(bundle string, uid types.UID, nodeIDs ...string) *Graph {
	isController := true
	g := &Graph{
		TypeMeta: metav1.TypeMeta{APIVersion: "kro.run/v1alpha1", Kind: "Graph"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-app-v1", Namespace: "default",
			Labels: map[string]string{"kardinal.io/bundle": bundle},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "kardinal.io/v1alpha1", Kind: "Bundle", Name: bundle, UID: uid,
				Controller: &isController,
			}},
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

func newFakeDynamic() *dynfake.FakeDynamicClient {
	return dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{GraphGVR: "GraphList"})
}

// TestGraphClient_CreateRefusesGraphOfAnotherOwner verifies that Create never
// takes over a Graph controlled by another Bundle, or by nothing (C01-graph-12).
func TestGraphClient_CreateRefusesGraphOfAnotherOwner(t *testing.T) {
	tests := []struct {
		name     string
		existing *Graph
	}{
		{name: "another bundle", existing: graphOwnedBy("bundle-a", "uid-a", "test", "prod")},
		{name: "no controller", existing: func() *Graph {
			g := graphOwnedBy("bundle-a", "uid-a", "test", "prod")
			g.OwnerReferences = nil
			return g
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			c := NewGraphClient(newFakeDynamic(), zerolog.Nop())
			require.NoError(t, c.Create(ctx, tt.existing))

			err := c.Create(ctx, graphOwnedBy("bundle-b", "uid-b", "test"))
			require.ErrorIs(t, err, ErrGraphOwnedByOther)

			got, getErr := c.Get(ctx, "default", "app-app-v1")
			require.NoError(t, getErr)
			assert.Len(t, got.Spec.Nodes, 2, "the existing spec must not be replaced")
			assert.Equal(t, "bundle-a", got.Labels["kardinal.io/bundle"])
			assert.Equal(t, tt.existing.OwnerReferences, got.OwnerReferences, "the Graph must not be re-parented")
		})
	}
}

// TestGraphClient_CreateRetriesUpdateConflict verifies that an optimistic-lock
// conflict on the in-place update is retried with a fresh read (C01-graph-13).
func TestGraphClient_CreateRetriesUpdateConflict(t *testing.T) {
	ctx := context.Background()
	dyn := newFakeDynamic()
	c := NewGraphClient(dyn, zerolog.Nop())
	require.NoError(t, c.Create(ctx, newTestGraph("test")))

	conflicts := 0
	dyn.PrependReactor("update", "graphs", func(k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts == 0 {
			conflicts++
			return true, nil, apierrors.NewConflict(GraphGVR.GroupResource(), "app-app-v1", errors.New("object was modified"))
		}
		return false, nil, nil
	})
	require.NoError(t, c.Create(ctx, newTestGraph("test", "prod")))
	assert.Equal(t, 1, conflicts)
	got, err := c.Get(ctx, "default", "app-app-v1")
	require.NoError(t, err)
	assert.Len(t, got.Spec.Nodes, 2)
}

// TestGraphClient_GraphExistsUsesStatusCodes verifies that NotFound is
// decided by the API status, not by the text of the error (C01-graph-15): a
// Forbidden error for a Graph whose name contains "404" is an error, not a
// missing Graph.
func TestGraphClient_GraphExistsUsesStatusCodes(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantFound bool
		wantErr   bool
	}{
		{name: "not found without the words", err: &apierrors.StatusError{ErrStatus: metav1.Status{
			Status: metav1.StatusFailure, Code: 404, Reason: metav1.StatusReasonNotFound, Message: "gone"}}},
		{name: "forbidden name containing 404", wantErr: true,
			err: apierrors.NewForbidden(GraphGVR.GroupResource(), "shop-404ab", errors.New("denied"))},
		{name: "found", wantFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			dyn := newFakeDynamic()
			c := NewGraphClient(dyn, zerolog.Nop())
			require.NoError(t, c.Create(ctx, newTestGraph("test")))
			if tt.err != nil {
				dyn.PrependReactor("get", "graphs", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, tt.err
				})
			}
			found, err := c.GraphExists(ctx, "default", "app-app-v1")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantFound, found)
		})
	}
}

// TestGraphClient_ListReturnsKardinalGraphs verifies List returns only the
// Graphs kardinal generated in the namespace.
func TestGraphClient_ListReturnsKardinalGraphs(t *testing.T) {
	ctx := context.Background()
	c := NewGraphClient(newFakeDynamic(), zerolog.Nop())
	require.NoError(t, c.Create(ctx, newTestGraph("test")))
	other := newTestGraph("x")
	other.Name = "someone-elses"
	other.Labels = nil
	require.NoError(t, c.Create(ctx, other))

	got, err := c.List(ctx, "default")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "app-app-v1", got[0].Name)
}
