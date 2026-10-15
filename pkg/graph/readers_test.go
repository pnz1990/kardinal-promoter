// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"context"
	"fmt"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/metadata"
	metafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"
)

// readerGraph is Graph name of Bundle name in ns, whose ref nodes read
// from reads.
func readerGraph(ns, name string, reads ...string) *Graph {
	g := graphOwnedBy(name, types.UID("uid-"+name), "step")
	g.Name, g.Namespace = name, ns
	for _, r := range reads {
		g.Spec.Nodes = append(g.Spec.Nodes, GraphNode{ID: "ref" + r, Ref: map[string]interface{}{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]interface{}{"name": "app", "namespace": r},
		}})
	}
	return g
}

// metadataOf is a fake metadata client holding the metadata of every Graph
// in dyn, as the API server serves it.
func metadataOf(t testing.TB, dyn *dynfake.FakeDynamicClient, ns string) *metafake.FakeMetadataClient {
	t.Helper()
	list, err := dyn.Resource(GraphGVR).Namespace(ns).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	s := runtime.NewScheme()
	require.NoError(t, metav1.AddMetaToScheme(s))
	var objs []runtime.Object
	for i := range list.Items {
		u := &list.Items[i]
		objs = append(objs, &metav1.PartialObjectMetadata{
			TypeMeta: metav1.TypeMeta{APIVersion: GraphGVK.GroupVersion().String(), Kind: GraphGVK.Kind},
			ObjectMeta: metav1.ObjectMeta{Name: u.GetName(), Namespace: u.GetNamespace(), Labels: u.GetLabels(),
				OwnerReferences: u.GetOwnerReferences()},
		})
	}
	return metafake.NewSimpleMetadataClient(s, objs...)
}

// metaRecorder records the Limit of each metadata List.
type metaRecorder struct {
	metadata.Interface
	limits *[]int64
}

func (m metaRecorder) Resource(gvr schema.GroupVersionResource) metadata.Getter {
	return getterRecorder{Getter: m.Interface.Resource(gvr), limits: m.limits}
}

type getterRecorder struct {
	metadata.Getter
	limits *[]int64
}

func (g getterRecorder) Namespace(ns string) metadata.ResourceInterface {
	return listRecorder{ResourceInterface: g.Getter.Namespace(ns), limits: g.limits}
}

type listRecorder struct {
	metadata.ResourceInterface
	limits *[]int64
}

func (l listRecorder) List(ctx context.Context, opts metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	*l.limits = append(*l.limits, opts.Limit)
	return l.ResourceInterface.List(ctx, opts)
}

func lists(actions []k8stesting.Action) []k8stesting.ListAction {
	var out []k8stesting.ListAction
	for _, a := range actions {
		if l, ok := a.(k8stesting.ListAction); ok && a.GetVerb() == "list" {
			out = append(out, l)
		}
	}
	return out
}

// TestGraphClient_Readers (#1685): Readers answers whether a Graph of the
// namespace reads a namespace from the reader labels, one metadata read of
// at most one Graph (Limit 1) per question, without reading a single Graph
// spec. In the scale soak each Graph cleanup read every Graph of the
// namespace (about 178, each a full kro spec): 119 GB, 17% of the
// controller's allocations. A namespace that still holds a Graph without
// the labels (an earlier controller made it) is read in full, as before.
// Covers PERF-GRAPHCLEANUP-02.
func TestGraphClient_Readers(t *testing.T) {
	const graphs = 178
	ctx := context.Background()
	dyn := newFakeDynamic()
	c := NewGraphClient(dyn, zerolog.Nop())
	for i := range graphs {
		reads := []string{"team-a", "argocd"}
		if i == 7 {
			reads = append(reads, "flux-system")
		}
		require.NoError(t, c.Create(ctx, readerGraph("team-a", fmt.Sprintf("app-%d", i), reads...)))
	}
	meta := metadataOf(t, dyn, "team-a")
	var limits []int64
	c.WithMetadata(metaRecorder{Interface: meta, limits: &limits})
	dyn.ClearActions()

	reads, err := c.Readers(ctx, "team-a")
	require.NoError(t, err)
	for ns, want := range map[string]bool{"argocd": true, "flux-system": true, "team-a": true, "monitoring": false} {
		got, err := reads(ns)
		require.NoError(t, err)
		assert.Equal(t, want, got, ns)
	}
	assert.Empty(t, lists(dyn.Actions()), "no Graph spec is read")
	ml := lists(meta.Actions())
	var selectors []string
	for _, l := range ml {
		selectors = append(selectors, l.GetListRestrictions().Labels.String())
	}
	// One read per namespace asked, and one for unlabelled Graphs, made only
	// because no labelled Graph reads monitoring.
	assert.ElementsMatch(t, []string{"kardinal.io/bundle,reads.kardinal.io/argocd", "kardinal.io/bundle,reads.kardinal.io/flux-system",
		"kardinal.io/bundle,reads.kardinal.io/team-a", "kardinal.io/bundle,reads.kardinal.io/monitoring",
		"kardinal.io/bundle,!kardinal.io/reader-labels"}, selectors)
	assert.Equal(t, []int64{1, 1, 1, 1, 1}, limits, "at most one Graph per read")

	// A Graph an earlier controller made has no reader labels: the
	// namespace's Graphs are read in full, so it still counts.
	legacy, err := toUnstructured(readerGraph("team-a", "legacy", "monitoring"))
	require.NoError(t, err)
	_, err = dyn.Resource(GraphGVR).Namespace("team-a").Create(ctx, legacy, metav1.CreateOptions{})
	require.NoError(t, err)
	c.WithMetadata(metadataOf(t, dyn, "team-a"))
	dyn.ClearActions()
	reads, err = c.Readers(ctx, "team-a")
	require.NoError(t, err)
	got, err := reads("monitoring")
	require.NoError(t, err)
	assert.True(t, got, "the unlabelled Graph's namespace is read")
	assert.Len(t, lists(dyn.Actions()), 1, "the namespace's Graphs are read in full once")

	// No metadata client: a full read, as before.
	plain := NewGraphClient(dyn, zerolog.Nop())
	reads, err = plain.Readers(ctx, "team-a")
	require.NoError(t, err)
	got, err = reads("flux-system")
	require.NoError(t, err)
	assert.True(t, got)
}

// TestGraphClient_CreateReaderLabels: Create labels a Graph with each
// namespace it reads and the reader-labels marker; an update in place
// replaces them with the new spec's (a stale one would keep a binding the
// Graph no longer needs) and keeps every other label. A Graph that reads a
// namespace that is not a valid label name gets no reader labels, so
// Readers reads its namespace in full. Covers PERF-GRAPHCLEANUP-02.
func TestGraphClient_CreateReaderLabels(t *testing.T) {
	ctx := context.Background()
	dyn := newFakeDynamic()
	c := NewGraphClient(dyn, zerolog.Nop())
	labels := func() map[string]string {
		t.Helper()
		u, err := dyn.Resource(GraphGVR).Namespace("team-a").Get(ctx, "app", metav1.GetOptions{})
		require.NoError(t, err)
		return u.GetLabels()
	}

	require.NoError(t, c.Create(ctx, readerGraph("team-a", "app", "argocd")))
	assert.Equal(t, map[string]string{"kardinal.io/bundle": "app", LabelReaderLabels: "true",
		LabelReadsPrefix + "argocd": "true"}, labels())

	u, err := dyn.Resource(GraphGVR).Namespace("team-a").Get(ctx, "app", metav1.GetOptions{})
	require.NoError(t, err)
	l := u.GetLabels()
	l["example.com/team"] = "payments"
	u.SetLabels(l)
	_, err = dyn.Resource(GraphGVR).Namespace("team-a").Update(ctx, u, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.NoError(t, c.Create(ctx, readerGraph("team-a", "app", "flux-system", "")))
	assert.Equal(t, map[string]string{"kardinal.io/bundle": "app", LabelReaderLabels: "true",
		LabelReadsPrefix + "flux-system": "true", LabelReadsPrefix + "team-a": "true",
		"example.com/team": "payments"}, labels(), "argocd's label goes, the foreign label stays")

	require.NoError(t, c.Create(ctx, readerGraph("team-a", "app", "${schema.spec.ns}")))
	assert.Equal(t, map[string]string{"kardinal.io/bundle": "app", "example.com/team": "payments"}, labels(),
		"no reader labels for a namespace that is not a label name")
	assert.Nil(t, ReaderLabels(readerGraph("team-a", "app", "Not_A_Namespace")))
}

// BenchmarkGraphClient_Readers: one Graph cleanup's prune question ("does a
// Graph of team-a still read argocd?") in a namespace of 178 Graphs of 20
// nodes each, the soak's count. full-list is the read before #1685, every
// Graph of the namespace; reader-labels the metadata reads. The fake
// metadata client ignores Limit and returns every matching Graph's
// metadata, so reader-labels is an upper bound: the API server returns one.
func BenchmarkGraphClient_Readers(b *testing.B) {
	ctx := context.Background()
	dyn := newFakeDynamic()
	c := NewGraphClient(dyn, zerolog.Nop())
	for i := range 178 {
		g := readerGraph("team-a", fmt.Sprintf("app-%d", i), "team-a", "argocd")
		for j := range 18 {
			g.Spec.Nodes = append(g.Spec.Nodes, GraphNode{ID: fmt.Sprintf("env%d", j), Template: map[string]interface{}{
				"apiVersion": "kardinal.io/v1alpha1", "kind": "PromotionStep",
				"metadata": map[string]interface{}{"name": fmt.Sprintf("app-%d-env%d", i, j),
					"labels": map[string]interface{}{"kardinal.io/pipeline": "app", "kardinal.io/environment": fmt.Sprintf("env%d", j)}},
				"spec": map[string]interface{}{"pipelineName": "app", "bundleName": "${bundle.metadata.name}",
					"environment": fmt.Sprintf("env%d", j), "stepType": "kustomize",
					"upstreamVerified": "${upstream.status.state == 'Verified'}"},
			}})
		}
		require.NoError(b, c.Create(ctx, g))
	}
	meta := metadataOf(b, dyn, "team-a")
	for _, tc := range []struct {
		name string
		c    *GraphClient
	}{
		{"full-list", NewGraphClient(dyn, zerolog.Nop())},
		{"reader-labels", NewGraphClient(dyn, zerolog.Nop()).WithMetadata(meta)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				reads, err := tc.c.Readers(ctx, "team-a")
				if err != nil {
					b.Fatal(err)
				}
				if ok, err := reads("argocd"); err != nil || !ok {
					b.Fatal("argocd must be read", err)
				}
			}
		})
	}
}

// pagedList serves pages in turn, as an API server that filters a labelled
// list can: an empty page with a continue token before the match.
type pagedList struct {
	metadata.ResourceInterface
	pages []metav1.PartialObjectMetadataList
	calls []metav1.ListOptions
}

func (p *pagedList) List(_ context.Context, opts metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	p.calls = append(p.calls, opts)
	page := p.pages[0]
	p.pages = p.pages[1:]
	return &page, nil
}

// TestAnyGraph_FollowsContinue: an empty page with a continue token is not
// "no Graph": anyGraph reads on until a Graph or the end.
func TestAnyGraph_FollowsContinue(t *testing.T) {
	found := metav1.PartialObjectMetadataList{Items: []metav1.PartialObjectMetadata{{ObjectMeta: metav1.ObjectMeta{Name: "g"}}}}
	tests := []struct {
		name  string
		pages []metav1.PartialObjectMetadataList
		want  bool
	}{
		{"match on the first page", []metav1.PartialObjectMetadataList{found}, true},
		{"match after an empty page", []metav1.PartialObjectMetadataList{{ListMeta: metav1.ListMeta{Continue: "c1"}}, found}, true},
		{"no match", []metav1.PartialObjectMetadataList{{ListMeta: metav1.ListMeta{Continue: "c1"}}, {}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &pagedList{pages: tc.pages}
			got, err := anyGraph(context.Background(), p, "kardinal.io/bundle")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Len(t, p.calls, len(tc.pages))
			for i, c := range p.calls {
				assert.Equal(t, int64(1), c.Limit)
				if i > 0 {
					assert.Equal(t, "c1", c.Continue)
				}
			}
		})
	}
}
