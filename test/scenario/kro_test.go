// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

func applyGraph(t *testing.T, w *World, nodes ...graph.GraphNode) {
	t.Helper()
	g := &graph.Graph{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "g"}, Spec: graph.GraphSpec{Nodes: nodes}}
	obj, err := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(g)
	require.NoError(t, err)
	u := &unstructured.Unstructured{Object: obj}
	u.SetGroupVersionKind(GraphGVK)
	cur := &unstructured.Unstructured{}
	cur.SetGroupVersionKind(GraphGVK)
	if err := w.Client.Get(w.Ctx, client.ObjectKey{Namespace: "ns", Name: "g"}, cur); err == nil {
		u.SetResourceVersion(cur.GetResourceVersion())
		require.NoError(t, w.Client.Update(w.Ctx, u))
	} else {
		require.True(t, apierrors.IsNotFound(err))
		require.NoError(t, w.Client.Create(w.Ctx, u))
	}
	_, err = w.Kro.Sync(w.Ctx)
	require.NoError(t, err)
}

func configMap(w *World, name string) (*corev1.ConfigMap, bool) {
	var cm corev1.ConfigMap
	err := w.Client.Get(w.Ctx, client.ObjectKey{Namespace: "ns", Name: name}, &cm)
	return &cm, err == nil
}

func cmTemplate(name string, data map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": name},
		"data": data}
}

// TestKro: the Graph executor follows kro's standalone Graph semantics. A
// node whose expression is data pending (here: a ref to an object that does
// not exist yet, then an index out of bounds) is not applied and neither are
// its dependents; once the data is there, they are. includeWhen false
// prunes what a node applied. A forEach node applies one object per item
// and prunes the items that go. An expression error that is not data
// pending is a hard error (Kro.Errors). A patch waits for its target.
// Covers SCEN-KRO-01.
func TestKro(t *testing.T) {
	w := NewWorld(t, Options{})
	src := graph.GraphNode{ID: "src", Ref: map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"name": "src", "namespace": "ns"}}}
	dep := graph.GraphNode{ID: "dep", Template: cmTemplate("dep", map[string]interface{}{
		"v": "${[src.data.v].filter(x_, src.data.ready == \"yes\")[0]}"})}
	patch := graph.GraphNode{ID: "mark", Patch: cmTemplate("dep", map[string]interface{}{"patched": "${src.data.v}"})}
	applyGraph(t, w, src, dep, patch)
	_, ok := configMap(w, "dep")
	assert.False(t, ok, "a missing ref holds its dependents")

	require.NoError(t, w.Client.Create(w.Ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "src"},
		Data: map[string]string{"v": "1", "ready": "no"}}))
	applyGraph(t, w, src, dep, patch)
	_, ok = configMap(w, "dep")
	assert.False(t, ok, "index out of bounds is data pending")

	src2, _ := configMap(w, "src")
	src2.Data["ready"] = "yes"
	require.NoError(t, w.Client.Update(w.Ctx, src2))
	applyGraph(t, w, src, dep, patch)
	_, _ = w.Kro.Sync(w.Ctx) // the patch target exists from this pass on
	cm, ok := configMap(w, "dep")
	require.True(t, ok)
	assert.Equal(t, "1", cm.Data["v"])
	assert.Equal(t, "1", cm.Data["patched"], "the patch applies once its target exists")
	assert.Equal(t, "dep", cm.Labels[graph.LabelKRONodeID])

	gated := dep
	gated.IncludeWhen = []string{"${src.data.v == \"2\"}"}
	applyGraph(t, w, src, gated)
	_, ok = configMap(w, "dep")
	assert.False(t, ok, "includeWhen false prunes the node's object")

	each := graph.GraphNode{ID: "items", ForEach: []map[string]string{{"it": "${[\"a\", \"b\"]}"}},
		Template: cmTemplate("${\"item-\" + it}", map[string]interface{}{"i": "${it}"})}
	applyGraph(t, w, src, each)
	for _, n := range []string{"item-a", "item-b"} {
		c, ok := configMap(w, n)
		require.True(t, ok, n)
		assert.Equal(t, "2", c.Labels["kro.run/collection-size"])
	}
	each.ForEach = []map[string]string{{"it": "${[\"a\"]}"}}
	applyGraph(t, w, src, each)
	_, ok = configMap(w, "item-b")
	assert.False(t, ok, "an item that goes is pruned")
	assert.Empty(t, w.Kro.Errors())

	bad := graph.GraphNode{ID: "bad", Template: cmTemplate("bad", map[string]interface{}{"v": "${src.data.v + 1}"})}
	applyGraph(t, w, src, bad)
	require.Len(t, w.Kro.Errors(), 1, "string + int is a hard error")
	assert.Contains(t, w.Kro.Errors()[0], `node "bad"`)
}

// TestKro_RejectsInvalidGraphs: the compiler rules kro applies before it
// runs a Graph: node IDs, exactly one of template/ref/def/patch, no
// unknown identifiers, no cycles. Covers SCEN-KRO-01.
func TestKro_RejectsInvalidGraphs(t *testing.T) {
	for name, nodes := range map[string][]graph.GraphNode{
		"bad id":      {{ID: "a-b", Def: map[string]interface{}{"x": 1}}},
		"two kinds":   {{ID: "a", Def: map[string]interface{}{"x": 1}, Template: cmTemplate("a", nil)}},
		"unknown ref": {{ID: "a", Def: map[string]interface{}{"x": "${nope.x}"}}},
		"cycle":       {{ID: "a", Def: map[string]interface{}{"x": "${b.x}"}}, {ID: "b", Def: map[string]interface{}{"x": "${a.x}"}}},
	} {
		t.Run(name, func(t *testing.T) {
			w := NewWorld(t, Options{})
			applyGraph(t, w, nodes...)
			assert.Len(t, w.Kro.Errors(), 1)
		})
	}
}
