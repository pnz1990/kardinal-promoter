// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
)

// renderRig is a Bundle reconciler with the real Translator and a fake Graph
// API, so a test reads the Graph each reconcile wrote.
type renderRig struct {
	c   client.Client
	dyn *dynfake.FakeDynamicClient
	r   *bundle.Reconciler
}

func newRenderRig(t *testing.T, objs ...client.Object) *renderRig {
	t.Helper()
	c := indexedBuilder(newScheme()).
		WithObjects(objs...).
		WithStatusSubresource(&kardinalv1alpha1.Bundle{}, &kardinalv1alpha1.Pipeline{}, &kardinalv1alpha1.PromotionStep{}).
		Build()
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{graph.GraphGVR: "GraphList"})
	gc := graph.NewGraphClient(dyn, zerolog.Nop())
	r := &bundle.Reconciler{Client: c, GraphChecker: gc,
		Translator: translator.New(gc, graph.NewBuilder(), c, nil, zerolog.Nop())}
	return &renderRig{c: c, dyn: dyn, r: r}
}

// updates counts the Graph updates for Bundle b of Pipeline app.
func (g *renderRig) updates(b string) int {
	name := graph.GraphNameFrom("app", b)
	n := 0
	for _, a := range g.dyn.Actions() {
		if a.GetVerb() != "update" {
			continue
		}
		w, ok := a.(k8stesting.UpdateAction)
		if !ok {
			continue
		}
		if u, ok := w.GetObject().(*unstructured.Unstructured); ok && u.GetName() == name {
			n++
		}
	}
	return n
}

// #2271: a Promoting Bundle whose Graph an earlier controller rendered (no
// kardinal.io/render-version label, or another one) is re-translated once,
// with no change to the Pipeline or a gate, and the Graph gets the current
// rendering and label; a second reconcile writes nothing, and a Verified
// Bundle keeps its Graph. The release/v0.12 port of main's
// TestRenderVersion_StuckVersionRetranslatesOnce.
func TestRenderVersion_StuckVersionRetranslatesOnce(t *testing.T) {
	ctx := context.Background()
	verify := gateTemplate("explore-qa", "prod", false)
	verify.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "explore", Required: 1}
	cab := gateTemplate("cab", "prod", false)
	cab.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: 1}
	p := lcPipeline("app", lcEnvs("test", "prod")...)
	p.Spec.Git.URL = "https://github.com/acme/app"
	b := lcBundle("app-v1", "image", "Available", time.Now().UTC().Add(-time.Hour))
	b.Spec.Images = []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/acme/app", Tag: "app-v1"}}
	g := newRenderRig(t, p, verify, cab, b)
	lcReconcile(t, g.r, "app-v1")
	require.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)
	gvr := g.dyn.Resource(graph.GraphGVR).Namespace("default")
	name := graph.GraphNameFrom("app", "app-v1")
	read := func() *unstructured.Unstructured {
		u, err := gvr.Get(ctx, name, metav1.GetOptions{})
		require.NoError(t, err)
		return u
	}
	nodeIDs := func(u *unstructured.Unstructured) []string {
		nodes, _, _ := unstructured.NestedSlice(u.Object, "spec", "nodes")
		var ids []string
		for _, n := range nodes {
			ids = append(ids, fmt.Sprint(n.(map[string]interface{})["id"]))
		}
		return ids
	}
	current := graph.RenderLabel
	require.Contains(t, nodeIDs(read()), graph.NodeVerifyOnlyGates)
	require.Equal(t, current, read().GetLabels()[graph.LabelRenderVersion], "the Graph carries its render version")

	// What v0.12.2 left: a Graph without that collection and without the
	// label.
	old := func() {
		u := read()
		nodes, _, _ := unstructured.NestedSlice(u.Object, "spec", "nodes")
		var kept []interface{}
		for _, n := range nodes {
			if n.(map[string]interface{})["id"] != graph.NodeVerifyOnlyGates {
				kept = append(kept, n)
			}
		}
		require.NoError(t, unstructured.SetNestedSlice(u.Object, kept, "spec", "nodes"))
		labels := u.GetLabels()
		delete(labels, graph.LabelRenderVersion)
		u.SetLabels(labels)
		_, err := gvr.Update(ctx, u, metav1.UpdateOptions{})
		require.NoError(t, err)
	}
	old()
	require.NotContains(t, nodeIDs(read()), graph.NodeVerifyOnlyGates)
	updates := g.updates("app-v1")

	lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, updates+1, g.updates("app-v1"), "re-translated once, with no Pipeline or gate change")
	assert.Contains(t, nodeIDs(read()), graph.NodeVerifyOnlyGates, "the Graph has the current rendering")
	assert.Equal(t, current, read().GetLabels()[graph.LabelRenderVersion])
	assert.Equal(t, "Promoting", lcGet(t, g.c, "app-v1").Status.Phase)

	lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, updates+1, g.updates("app-v1"), "once")

	// A Verified Bundle keeps the Graph it was promoted with.
	v := lcGet(t, g.c, "app-v1")
	v.Status.Phase = "Verified"
	require.NoError(t, g.c.Status().Update(ctx, &v))
	old()
	updates = g.updates("app-v1")
	lcReconcile(t, g.r, "app-v1")
	assert.Equal(t, updates, g.updates("app-v1"), "a Verified Bundle is not re-translated")
	assert.NotContains(t, nodeIDs(read()), graph.NodeVerifyOnlyGates)
}
