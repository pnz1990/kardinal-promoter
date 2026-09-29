// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// makeTestGraph builds a minimal Graph with one PromotionStep node per environment.
// Mirrors the convention used by graph.Builder: node ID = graph.CELSafeSlug(envName).
func makeTestGraph(envNames ...string) *graph.Graph {
	g := &graph.Graph{
		ObjectMeta: metav1.ObjectMeta{Name: "test-graph", Namespace: "default"},
	}
	for _, name := range envNames {
		g.Spec.Nodes = append(g.Spec.Nodes, graph.GraphNode{
			ID: graph.CELSafeSlug(name),
			Template: map[string]interface{}{
				"apiVersion": "kardinal.io/v1alpha1",
				"kind":       "PromotionStep",
				"metadata":   map[string]interface{}{"name": "test-" + name},
				"spec":       map[string]interface{}{"environment": name},
			},
			ReadyWhen: []string{
				`${` + graph.CELSafeSlug(name) + `.status.state == "Verified"}`,
			},
		})
	}
	return g
}

// envNames returns the names of the pipeline's environments.
func envNames(p *kardinalv1alpha1.Pipeline) []string {
	if p == nil {
		return nil
	}
	names := make([]string, 0, len(p.Spec.Environments))
	for _, e := range p.Spec.Environments {
		names = append(names, e.Name)
	}
	return names
}

// injectAll injects health ref nodes for every environment of p, with every
// kind served and every namespace readable, and returns how many it added.
func injectAll(p *kardinalv1alpha1.Pipeline, g *graph.Graph) int {
	return len(healthInjector{log: zerolog.Nop()}.inject(p, g, envNames(p)))
}

// makePipeline builds a minimal Pipeline with the given environments.
func makePipeline(name string, envs []kardinalv1alpha1.EnvironmentSpec) *kardinalv1alpha1.Pipeline {
	return &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
	}
}

// TestInjectHealthWatchNodes_NoHealthType verifies that environments without
// health.type do not produce ref nodes.
func TestInjectHealthWatchNodes_NoHealthType(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "prod"},
	})
	g := makeTestGraph("test", "prod")
	originalCount := len(g.Spec.Nodes)

	injected := injectAll(pipeline, g)
	assert.Equal(t, 0, injected, "no ref nodes should be injected when health.type is empty")
	assert.Len(t, g.Spec.Nodes, originalCount, "node count must not change")
}

// TestInjectHealthWatchNodes_Resource verifies that health.type=resource injects
// a kro ref node for apps/v1 Deployment.
func TestInjectHealthWatchNodes_Resource(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "resource"}},
	})
	g := makeTestGraph("prod")
	originalStepCount := len(g.Spec.Nodes)

	injected := injectAll(pipeline, g)
	assert.Equal(t, 1, injected)
	assert.Len(t, g.Spec.Nodes, originalStepCount+1, "one ref node added")

	// Find the health ref node
	var watchNode *graph.GraphNode
	for i := range g.Spec.Nodes {
		if strings.HasPrefix(g.Spec.Nodes[i].ID, "health") {
			watchNode = &g.Spec.Nodes[i]
			break
		}
	}
	require.NotNil(t, watchNode, "health ref node must exist")

	// Node ID follows "health<TitleCaseEnvSlug>" camelCase pattern
	assert.Equal(t, "healthProd", watchNode.ID)

	// Template must be identity-only (kro NodeRef shape):
	// Only apiVersion, kind, metadata.name/namespace
	tmpl := watchNode.Ref
	assert.Equal(t, "apps/v1", tmpl["apiVersion"])
	assert.Equal(t, "Deployment", tmpl["kind"])
	md := tmpl["metadata"].(map[string]interface{})
	assert.Equal(t, "nginx", md["name"], "deployment name = pipeline name")
	assert.Equal(t, "prod", md["namespace"], "deployment namespace = env name")

	// Template must NOT have spec or other fields (a ref reads, it does not render)
	_, hasSpec := tmpl["spec"]
	assert.False(t, hasSpec, "identity-only template must not have spec field")
	for k := range tmpl {
		assert.Contains(t, []string{"apiVersion", "kind", "metadata"}, k,
			"identity-only template must only have apiVersion/kind/metadata")
	}
	for k := range md {
		assert.Contains(t, []string{"name", "namespace"}, k,
			"metadata must only have name/namespace for a named ref")
	}

	// ReadyWhen must reference the actual node ID (not the placeholder "healthNode")
	require.NotEmpty(t, watchNode.ReadyWhen)
	assert.Contains(t, watchNode.ReadyWhen[0], "healthProd", "readyWhen must reference node ID")
	assert.NotContains(t, watchNode.ReadyWhen[0], "healthNode", "placeholder must be substituted")
	assert.Contains(t, watchNode.ReadyWhen[0], "Available", "readyWhen checks Available condition")
}

// TestInjectHealthWatchNodes_ArgoCD verifies health.type=argocd ref node.
func TestInjectHealthWatchNodes_ArgoCD(t *testing.T) {
	pipeline := makePipeline("myapp", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "argocd"}},
	})
	g := makeTestGraph("prod")

	injected := injectAll(pipeline, g)
	assert.Equal(t, 1, injected)

	watchNode := findHealthNode(g, "prod")
	require.NotNil(t, watchNode)

	tmpl := watchNode.Ref
	assert.Equal(t, "argoproj.io/v1alpha1", tmpl["apiVersion"])
	assert.Equal(t, "Application", tmpl["kind"])
	md := tmpl["metadata"].(map[string]interface{})
	assert.Equal(t, "myapp-prod", md["name"], "application name = pipeline + env")
	assert.Equal(t, "argocd", md["namespace"])

	require.NotEmpty(t, watchNode.ReadyWhen)
	assert.Contains(t, watchNode.ReadyWhen[0], "Healthy")
	assert.Contains(t, watchNode.ReadyWhen[0], "Synced")
}

// TestInjectHealthWatchNodes_Flux verifies health.type=flux ref node.
func TestInjectHealthWatchNodes_Flux(t *testing.T) {
	pipeline := makePipeline("myapp", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "staging", Health: kardinalv1alpha1.HealthConfig{Type: "flux"}},
	})
	g := makeTestGraph("staging")

	injected := injectAll(pipeline, g)
	assert.Equal(t, 1, injected)

	watchNode := findHealthNode(g, "staging")
	require.NotNil(t, watchNode)

	tmpl := watchNode.Ref
	assert.Equal(t, "kustomize.toolkit.fluxcd.io/v1", tmpl["apiVersion"])
	assert.Equal(t, "Kustomization", tmpl["kind"])
	md := tmpl["metadata"].(map[string]interface{})
	assert.Equal(t, "myapp-staging", md["name"])
	assert.Equal(t, "flux-system", md["namespace"])

	require.NotEmpty(t, watchNode.ReadyWhen)
	assert.Contains(t, watchNode.ReadyWhen[0], "Ready")
}

// TestInjectHealthWatchNodes_ArgoRollouts verifies health.type=argoRollouts ref node.
func TestInjectHealthWatchNodes_ArgoRollouts(t *testing.T) {
	pipeline := makePipeline("rollouts-demo", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod-eu", Health: kardinalv1alpha1.HealthConfig{Type: "argoRollouts"}},
	})
	g := makeTestGraph("prod-eu")

	injected := injectAll(pipeline, g)
	assert.Equal(t, 1, injected)

	watchNode := findHealthNode(g, "prod-eu")
	require.NotNil(t, watchNode)
	assert.Equal(t, "healthProdEu", watchNode.ID, "hyphens in env name become camelCase word boundaries")

	tmpl := watchNode.Ref
	assert.Equal(t, "argoproj.io/v1alpha1", tmpl["apiVersion"])
	assert.Equal(t, "Rollout", tmpl["kind"])
	md := tmpl["metadata"].(map[string]interface{})
	assert.Equal(t, "rollouts-demo", md["name"])
	assert.Equal(t, "prod-eu", md["namespace"])
}

// TestInjectHealthWatchNodes_Flagger verifies health.type=flagger ref node.
func TestInjectHealthWatchNodes_Flagger(t *testing.T) {
	pipeline := makePipeline("myapp", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "flagger"}},
	})
	g := makeTestGraph("prod")

	injected := injectAll(pipeline, g)
	assert.Equal(t, 1, injected)

	watchNode := findHealthNode(g, "prod")
	require.NotNil(t, watchNode)

	tmpl := watchNode.Ref
	assert.Equal(t, "flagger.app/v1beta1", tmpl["apiVersion"])
	assert.Equal(t, "Canary", tmpl["kind"])
	assert.Contains(t, watchNode.ReadyWhen[0], "Succeeded")
}

// TestInjectHealthWatchNodes_MultipleEnvs verifies multiple environments each get
// their own ref node.
func TestInjectHealthWatchNodes_MultipleEnvs(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"}, // no health
		{Name: "uat", Health: kardinalv1alpha1.HealthConfig{Type: "resource"}}, // has health
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "argocd"}},  // has health
	})
	g := makeTestGraph("test", "uat", "prod")
	originalCount := len(g.Spec.Nodes)

	injected := injectAll(pipeline, g)
	assert.Equal(t, 2, injected, "test has no health.type, uat and prod do")
	assert.Len(t, g.Spec.Nodes, originalCount+2)

	uatNode := findHealthNode(g, "uat")
	require.NotNil(t, uatNode)
	assert.Equal(t, "healthUat", uatNode.ID)

	prodNode := findHealthNode(g, "prod")
	require.NotNil(t, prodNode)
	assert.Equal(t, "healthProd", prodNode.ID)
}

// TestInjectHealthWatchNodes_PromotionStepReadyWhenUnchanged verifies that the
// companion PromotionStep node's readyWhen is left alone: kro readyWhen may
// only reference the node itself (ledger gap G3). The health ref node carries
// its own readyWhen, and the PromotionStep reconciler reads health directly.
func TestInjectHealthWatchNodes_PromotionStepReadyWhenUnchanged(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "resource"}},
	})
	g := makeTestGraph("prod")
	orig := append([]string{}, g.Spec.Nodes[0].ReadyWhen...)

	injected := injectAll(pipeline, g)
	assert.Equal(t, 1, injected)

	var stepNode *graph.GraphNode
	for i := range g.Spec.Nodes {
		if g.Spec.Nodes[i].ID == "prod" {
			stepNode = &g.Spec.Nodes[i]
			break
		}
	}
	require.NotNil(t, stepNode)
	assert.Equal(t, orig, stepNode.ReadyWhen,
		"PromotionStep readyWhen must not reference the health node")
}

// TestInjectHealthWatchNodes_NilGraph handles nil input gracefully.
func TestInjectHealthWatchNodes_NilGraph(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "resource"}},
	})
	injected := injectAll(pipeline, nil)
	assert.Equal(t, 0, injected, "nil graph must return 0 without error")
}

// TestInjectHealthWatchNodes_NilPipeline handles nil pipeline gracefully.
func TestInjectHealthWatchNodes_NilPipeline(t *testing.T) {
	g := makeTestGraph("prod")
	injected := injectAll(nil, g)
	assert.Equal(t, 0, injected, "nil pipeline must return 0 without error")
}

// TestInjectHealthWatchNodes_UnknownHealthType skips unknown types with a
// warning instead of failing the translation (C01-graph-25).
func TestInjectHealthWatchNodes_UnknownHealthType(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "unknown-type"}},
	})
	g := makeTestGraph("prod")
	originalCount := len(g.Spec.Nodes)

	injected := injectAll(pipeline, g)
	assert.Equal(t, 0, injected, "unknown health type is skipped")
	assert.Len(t, g.Spec.Nodes, originalCount, "node count unchanged for unknown type")
}

// TestInjectHealthWatchNodes_WatchNodeIsIdentityOnly verifies that the emitted
// kro ref node carries only apiVersion, kind, metadata.name/namespace — the
// shape of a kro NodeRef.
func TestInjectHealthWatchNodes_WatchNodeIsIdentityOnly(t *testing.T) {
	tests := []struct {
		name       string
		healthType string
	}{
		{"resource", "resource"},
		{"argocd", "argocd"},
		{"flux", "flux"},
		{"argoRollouts", "argoRollouts"},
		{"flagger", "flagger"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := makePipeline("myapp", []kardinalv1alpha1.EnvironmentSpec{
				{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: tc.healthType}},
			})
			g := makeTestGraph("prod")

			injectAll(pipeline, g)

			watchNode := findHealthNode(g, "prod")
			require.NotNil(t, watchNode, "health ref node must exist for type %s", tc.healthType)

			tmpl := watchNode.Ref

			// Only allowed top-level keys: apiVersion, kind, metadata
			for k := range tmpl {
				assert.Contains(t, []string{"apiVersion", "kind", "metadata"}, k,
					"ref must only have apiVersion/kind/metadata, got: %s", k)
			}

			// metadata must only have name/namespace
			md, ok := tmpl["metadata"].(map[string]interface{})
			require.True(t, ok, "metadata must be a map")
			for k := range md {
				assert.Contains(t, []string{"name", "namespace"}, k,
					"ref metadata must only have name/namespace, got: %s", k)
			}

			// apiVersion and kind must be present
			assert.NotEmpty(t, tmpl["apiVersion"])
			assert.NotEmpty(t, tmpl["kind"])
			assert.NotEmpty(t, md["name"])
			assert.NotEmpty(t, md["namespace"])
		})
	}
}

// findHealthNode finds the health ref node for a given environment in the graph.
func findHealthNode(g *graph.Graph, envName string) *graph.GraphNode {
	target := "health" + strings.ToUpper(graph.CELSafeSlug(envName)[:1]) + graph.CELSafeSlug(envName)[1:]
	for i := range g.Spec.Nodes {
		if g.Spec.Nodes[i].ID == target {
			return &g.Spec.Nodes[i]
		}
	}
	return nil
}

// TestInjectHealthWatchNodes_ResourceWatchKind verifies that health.type=resource with
// health.labelSelector emits a collection ref node (metadata.selector, no metadata.name)
// with a per-element readyWhen on each.
func TestInjectHealthWatchNodes_ResourceWatchKind(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{
			Name: "prod",
			Health: kardinalv1alpha1.HealthConfig{
				Type: "resource",
				LabelSelector: map[string]string{
					"app":                  "nginx",
					"kardinal.io/pipeline": "nginx",
				},
			},
		},
	})
	g := makeTestGraph("prod")

	injected := injectAll(pipeline, g)
	assert.Equal(t, 1, injected)

	watchNode := findHealthNode(g, "prod")
	require.NotNil(t, watchNode, "health node for prod must be present")

	// Collection ref: metadata.selector instead of metadata.name.
	tmpl := watchNode.Ref
	assert.Equal(t, "apps/v1", tmpl["apiVersion"])
	assert.Equal(t, "Deployment", tmpl["kind"])

	md, hasMd := tmpl["metadata"].(map[string]interface{})
	require.True(t, hasMd, "collection ref must have metadata")
	_, hasName := md["name"]
	assert.False(t, hasName, "collection ref must NOT have metadata.name")
	assert.Equal(t, "prod", md["namespace"], "collection ref must scope to the env namespace")

	sel, ok := md["selector"].(map[string]interface{})
	require.True(t, ok, "collection ref must have metadata.selector")
	matchLabels, ok := sel["matchLabels"].(map[string]interface{})
	require.True(t, ok, "selector must be a LabelSelector with matchLabels")
	assert.Equal(t, "nginx", matchLabels["app"])
	assert.Equal(t, "nginx", matchLabels["kardinal.io/pipeline"])

	// kro evaluates collection readyWhen per element bound to "each".
	require.Len(t, watchNode.ReadyWhen, 1)
	assert.True(t, strings.HasPrefix(watchNode.ReadyWhen[0], "${each.status.conditions.exists("),
		"collection readyWhen must be a per-element expression on each: %s", watchNode.ReadyWhen[0])
	assert.NotContains(t, watchNode.ReadyWhen[0], "healthNode", "placeholder must be substituted")
}

// TestInjectHealthWatchNodes_ResourceWatchKindVsWatch verifies that adding a LabelSelector
// switches from a named ref to a collection ref without affecting the named case.
func TestInjectHealthWatchNodes_ResourceWatchKindVsWatch(t *testing.T) {
	// Named case: no LabelSelector
	watchPipeline := makePipeline("myapp", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "uat", Health: kardinalv1alpha1.HealthConfig{Type: "resource"}},
	})
	gWatch := makeTestGraph("uat")
	injectAll(watchPipeline, gWatch)

	watchNode := findHealthNode(gWatch, "uat")
	require.NotNil(t, watchNode)
	// Named ref: must have metadata.name
	tmplWatch := watchNode.Ref
	md := tmplWatch["metadata"].(map[string]interface{})
	assert.Equal(t, "myapp", md["name"])
	assert.Contains(t, watchNode.ReadyWhen[0], "healthUat.status.conditions.exists(")

	// Collection case: with LabelSelector
	watchKindPipeline := makePipeline("myapp", []kardinalv1alpha1.EnvironmentSpec{
		{
			Name: "uat",
			Health: kardinalv1alpha1.HealthConfig{
				Type:          "resource",
				LabelSelector: map[string]string{"app": "myapp"},
			},
		},
	})
	gWatchKind := makeTestGraph("uat")
	injectAll(watchKindPipeline, gWatchKind)

	watchKindNode := findHealthNode(gWatchKind, "uat")
	require.NotNil(t, watchKindNode)
	// Collection ref: metadata.selector, no metadata.name.
	wkMd, hasMd := watchKindNode.Ref["metadata"].(map[string]interface{})
	require.True(t, hasMd, "collection ref must have metadata")
	_, hasName := wkMd["name"]
	assert.False(t, hasName, "collection ref must not have metadata.name")
	assert.Equal(t, "uat", wkMd["namespace"], "collection ref must scope to env namespace")
	_, hasSelector := wkMd["selector"]
	assert.True(t, hasSelector, "collection ref must have metadata.selector")
	assert.Contains(t, watchKindNode.ReadyWhen[0], "each.status.conditions.exists(",
		"collection readyWhen must be per element")
}

// TestInjectHealthWatchNodes_ResourceRef verifies that health.type=resource with
// health.resource set uses the specified name and namespace instead of the defaults.
// This exercises the fix for PDCA S9 — the `resource:` sub-field was rejected
// as an unknown field before ResourceRef was added to HealthConfig.
func TestInjectHealthWatchNodes_ResourceRef(t *testing.T) {
	pipeline := makePipeline("my-pipeline", []kardinalv1alpha1.EnvironmentSpec{
		{
			Name: "test",
			Health: kardinalv1alpha1.HealthConfig{
				Type: "resource",
				Resource: &kardinalv1alpha1.ResourceRef{
					Kind:      "Deployment",
					Name:      "custom-app",
					Namespace: "custom-ns",
				},
			},
		},
	})
	g := makeTestGraph("test")

	injected := injectAll(pipeline, g)
	assert.Equal(t, 1, injected)

	var watchNode *graph.GraphNode
	for i := range g.Spec.Nodes {
		if strings.HasPrefix(g.Spec.Nodes[i].ID, "health") {
			watchNode = &g.Spec.Nodes[i]
			break
		}
	}
	require.NotNil(t, watchNode, "health ref node must exist")

	tmpl := watchNode.Ref
	assert.Equal(t, "apps/v1", tmpl["apiVersion"])
	assert.Equal(t, "Deployment", tmpl["kind"])
	md := tmpl["metadata"].(map[string]interface{})
	// ResourceRef.Name overrides default (pipeline name "my-pipeline")
	assert.Equal(t, "custom-app", md["name"], "resource name from ResourceRef.Name")
	// ResourceRef.Namespace overrides default (env name "test")
	assert.Equal(t, "custom-ns", md["namespace"], "resource namespace from ResourceRef.Namespace")
}

// TestInjectHealthWatchNodes_ResourceRef_Defaults verifies that health.resource with
// only partial values falls back to defaults for unset fields.
func TestInjectHealthWatchNodes_ResourceRef_Defaults(t *testing.T) {
	pipeline := makePipeline("my-pipeline", []kardinalv1alpha1.EnvironmentSpec{
		{
			Name: "staging",
			Health: kardinalv1alpha1.HealthConfig{
				Type: "resource",
				Resource: &kardinalv1alpha1.ResourceRef{
					// Only namespace is set — name should default to pipeline name
					Namespace: "staging-infra",
				},
			},
		},
	})
	g := makeTestGraph("staging")

	injected := injectAll(pipeline, g)
	assert.Equal(t, 1, injected)

	var watchNode *graph.GraphNode
	for i := range g.Spec.Nodes {
		if strings.HasPrefix(g.Spec.Nodes[i].ID, "health") {
			watchNode = &g.Spec.Nodes[i]
			break
		}
	}
	require.NotNil(t, watchNode)

	md := watchNode.Ref["metadata"].(map[string]interface{})
	// Name defaults to pipeline name when ResourceRef.Name is empty
	assert.Equal(t, "my-pipeline", md["name"], "empty ResourceRef.Name falls back to pipeline name")
	// Namespace from ResourceRef.Namespace
	assert.Equal(t, "staging-infra", md["namespace"], "ResourceRef.Namespace overrides env name")
}

// TestInjectHealthNodes_SkipsUnservedKinds verifies that a health ref whose
// kind the cluster does not serve is dropped: kro fails the whole Graph
// compile on a ref to a missing CRD (ledger gap G4).
func TestInjectHealthNodes_SkipsUnservedKinds(t *testing.T) {
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, meta.RESTScopeNamespace)
	tr := (&Translator{}).WithRESTMapper(mapper)

	pipeline := makePipeline("myapp", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "uat", Health: kardinalv1alpha1.HealthConfig{Type: "resource"}},
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "argocd"}},
	})
	g := makeTestGraph("uat", "prod")

	injected := len(healthInjector{served: tr.servedKind}.inject(pipeline, g, envNames(pipeline)))
	assert.Equal(t, 1, injected, "argocd Application is not served and must be skipped")
	assert.NotNil(t, findHealthNode(g, "uat"))
	assert.Nil(t, findHealthNode(g, "prod"))
}
