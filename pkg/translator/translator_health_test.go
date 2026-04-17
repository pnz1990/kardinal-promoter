// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package translator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// makeTestGraph builds a minimal Graph with one PromotionStep node per environment.
// Mirrors the convention used by graph.Builder: node ID = celSafeSlug(envName).
func makeTestGraph(envNames ...string) *graph.Graph {
	g := &graph.Graph{
		ObjectMeta: metav1.ObjectMeta{Name: "test-graph", Namespace: "default"},
	}
	for _, name := range envNames {
		g.Spec.Nodes = append(g.Spec.Nodes, graph.GraphNode{
			ID: celSafeSlug(name),
			Template: map[string]interface{}{
				"apiVersion": "kardinal.io/v1alpha1",
				"kind":       "PromotionStep",
				"metadata":   map[string]interface{}{"name": "test-" + name},
				"spec":       map[string]interface{}{"environment": name},
			},
			ReadyWhen: []string{
				`${` + celSafeSlug(name) + `.status.state == "Verified"}`,
			},
			PropagateWhen: []string{
				`${` + celSafeSlug(name) + `.status.state == "Verified"}`,
			},
		})
	}
	return g
}

// makePipeline builds a minimal Pipeline with the given environments.
func makePipeline(name string, envs []kardinalv1alpha1.EnvironmentSpec) *kardinalv1alpha1.Pipeline {
	return &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
	}
}

// TestInjectHealthWatchNodes_NoHealthType verifies that environments without
// health.type do not produce Watch nodes.
func TestInjectHealthWatchNodes_NoHealthType(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "prod"},
	})
	g := makeTestGraph("test", "prod")
	originalCount := len(g.Spec.Nodes)

	injected, err := injectHealthWatchNodes(pipeline, g)
	require.NoError(t, err)
	assert.Equal(t, 0, injected, "no Watch nodes should be injected when health.type is empty")
	assert.Len(t, g.Spec.Nodes, originalCount, "node count must not change")
}

// TestInjectHealthWatchNodes_Resource verifies that health.type=resource injects
// a ShapeWatch node for apps/v1 Deployment.
func TestInjectHealthWatchNodes_Resource(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "resource"}},
	})
	g := makeTestGraph("prod")
	originalStepCount := len(g.Spec.Nodes)

	injected, err := injectHealthWatchNodes(pipeline, g)
	require.NoError(t, err)
	assert.Equal(t, 1, injected)
	assert.Len(t, g.Spec.Nodes, originalStepCount+1, "one Watch node added")

	// Find the health Watch node
	var watchNode *graph.GraphNode
	for i := range g.Spec.Nodes {
		if strings.HasPrefix(g.Spec.Nodes[i].ID, "health") {
			watchNode = &g.Spec.Nodes[i]
			break
		}
	}
	require.NotNil(t, watchNode, "health Watch node must exist")

	// Node ID follows "health<TitleCaseEnvSlug>" camelCase pattern
	assert.Equal(t, "healthProd", watchNode.ID)

	// Template must be identity-only (Watch-reference auto-detection):
	// Only apiVersion, kind, metadata.name/namespace
	tmpl := watchNode.Template
	assert.Equal(t, "apps/v1", tmpl["apiVersion"])
	assert.Equal(t, "Deployment", tmpl["kind"])
	md := tmpl["metadata"].(map[string]interface{})
	assert.Equal(t, "nginx", md["name"], "deployment name = pipeline name")
	assert.Equal(t, "prod", md["namespace"], "deployment namespace = env name")

	// Template must NOT have spec or other fields (would make it Own/Contribute reference)
	_, hasSpec := tmpl["spec"]
	assert.False(t, hasSpec, "identity-only template must not have spec field")
	for k := range tmpl {
		assert.Contains(t, []string{"apiVersion", "kind", "metadata"}, k,
			"identity-only template must only have apiVersion/kind/metadata")
	}
	for k := range md {
		assert.Contains(t, []string{"name", "namespace"}, k,
			"metadata must only have name/namespace for Watch-reference detection")
	}

	// krocodile >= 81c5a03 (now 05db829): Watch/Ref nodes must NOT have ReadyWhen.
	assert.Empty(t, watchNode.ReadyWhen, "Watch/Ref node must have no ReadyWhen (krocodile >= 81c5a03 compat)")
	assert.Empty(t, watchNode.PropagateWhen, "Watch/Ref node must have no PropagateWhen")
}

// TestInjectHealthWatchNodes_ArgoCD verifies health.type=argocd Watch node.
func TestInjectHealthWatchNodes_ArgoCD(t *testing.T) {
	pipeline := makePipeline("myapp", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "argocd"}},
	})
	g := makeTestGraph("prod")

	injected, err := injectHealthWatchNodes(pipeline, g)
	require.NoError(t, err)
	assert.Equal(t, 1, injected)

	watchNode := findHealthNode(g, "prod")
	require.NotNil(t, watchNode)

	tmpl := watchNode.Template
	assert.Equal(t, "argoproj.io/v1alpha1", tmpl["apiVersion"])
	assert.Equal(t, "Application", tmpl["kind"])
	md := tmpl["metadata"].(map[string]interface{})
	assert.Equal(t, "myapp-prod", md["name"], "application name = pipeline + env")
	assert.Equal(t, "argocd", md["namespace"])

	// krocodile >= 81c5a03: Watch/Ref node must NOT have ReadyWhen.
	assert.Empty(t, watchNode.ReadyWhen, "Watch/Ref node must have no ReadyWhen")
}

// TestInjectHealthWatchNodes_Flux verifies health.type=flux Watch node.
func TestInjectHealthWatchNodes_Flux(t *testing.T) {
	pipeline := makePipeline("myapp", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "staging", Health: kardinalv1alpha1.HealthConfig{Type: "flux"}},
	})
	g := makeTestGraph("staging")

	injected, err := injectHealthWatchNodes(pipeline, g)
	require.NoError(t, err)
	assert.Equal(t, 1, injected)

	watchNode := findHealthNode(g, "staging")
	require.NotNil(t, watchNode)

	tmpl := watchNode.Template
	assert.Equal(t, "kustomize.toolkit.fluxcd.io/v1", tmpl["apiVersion"])
	assert.Equal(t, "Kustomization", tmpl["kind"])
	md := tmpl["metadata"].(map[string]interface{})
	assert.Equal(t, "myapp-staging", md["name"])
	assert.Equal(t, "flux-system", md["namespace"])

	// krocodile >= 81c5a03: Watch/Ref node must NOT have ReadyWhen.
	assert.Empty(t, watchNode.ReadyWhen, "Watch/Ref node must have no ReadyWhen")
}

// TestInjectHealthWatchNodes_ArgoRollouts verifies health.type=argoRollouts Watch node.
func TestInjectHealthWatchNodes_ArgoRollouts(t *testing.T) {
	pipeline := makePipeline("rollouts-demo", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod-eu", Health: kardinalv1alpha1.HealthConfig{Type: "argoRollouts"}},
	})
	g := makeTestGraph("prod-eu")

	injected, err := injectHealthWatchNodes(pipeline, g)
	require.NoError(t, err)
	assert.Equal(t, 1, injected)

	watchNode := findHealthNode(g, "prod-eu")
	require.NotNil(t, watchNode)
	assert.Equal(t, "healthProdEu", watchNode.ID, "hyphens in env name become camelCase word boundaries")

	tmpl := watchNode.Template
	assert.Equal(t, "argoproj.io/v1alpha1", tmpl["apiVersion"])
	assert.Equal(t, "Rollout", tmpl["kind"])
	md := tmpl["metadata"].(map[string]interface{})
	assert.Equal(t, "rollouts-demo", md["name"])
	assert.Equal(t, "prod-eu", md["namespace"])
}

// TestInjectHealthWatchNodes_Flagger verifies health.type=flagger Watch node.
func TestInjectHealthWatchNodes_Flagger(t *testing.T) {
	pipeline := makePipeline("myapp", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "flagger"}},
	})
	g := makeTestGraph("prod")

	injected, err := injectHealthWatchNodes(pipeline, g)
	require.NoError(t, err)
	assert.Equal(t, 1, injected)

	watchNode := findHealthNode(g, "prod")
	require.NotNil(t, watchNode)

	tmpl := watchNode.Template
	assert.Equal(t, "flagger.app/v1beta1", tmpl["apiVersion"])
	assert.Equal(t, "Canary", tmpl["kind"])
	// krocodile >= 81c5a03: Watch/Ref node must NOT have ReadyWhen.
	assert.Empty(t, watchNode.ReadyWhen, "Watch/Ref node must have no ReadyWhen")
}

// TestInjectHealthWatchNodes_MultipleEnvs verifies multiple environments each get
// their own Watch node.
func TestInjectHealthWatchNodes_MultipleEnvs(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"}, // no health
		{Name: "uat", Health: kardinalv1alpha1.HealthConfig{Type: "resource"}}, // has health
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "argocd"}},  // has health
	})
	g := makeTestGraph("test", "uat", "prod")
	originalCount := len(g.Spec.Nodes)

	injected, err := injectHealthWatchNodes(pipeline, g)
	require.NoError(t, err)
	assert.Equal(t, 2, injected, "test has no health.type, uat and prod do")
	assert.Len(t, g.Spec.Nodes, originalCount+2)

	uatNode := findHealthNode(g, "uat")
	require.NotNil(t, uatNode)
	assert.Equal(t, "healthUat", uatNode.ID)

	prodNode := findHealthNode(g, "prod")
	require.NotNil(t, prodNode)
	assert.Equal(t, "healthProd", prodNode.ID)
}

// TestInjectHealthWatchNodes_PromotionStepReadyWhenUpdated verifies that the
// companion PromotionStep node's readyWhen gains the health Watch condition.
func TestInjectHealthWatchNodes_PromotionStepReadyWhenUpdated(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "resource"}},
	})
	g := makeTestGraph("prod")

	// Capture original readyWhen
	var origReadyWhen []string
	for _, node := range g.Spec.Nodes {
		if node.ID == "prod" {
			origReadyWhen = append(origReadyWhen, node.ReadyWhen...)
			break
		}
	}

	injected, err := injectHealthWatchNodes(pipeline, g)
	require.NoError(t, err)
	assert.Equal(t, 1, injected)

	// Find the PromotionStep node
	var stepNode *graph.GraphNode
	for i := range g.Spec.Nodes {
		if g.Spec.Nodes[i].ID == "prod" {
			stepNode = &g.Spec.Nodes[i]
			break
		}
	}
	require.NotNil(t, stepNode)

	// readyWhen must have grown by 1 (health condition appended)
	assert.Len(t, stepNode.ReadyWhen, len(origReadyWhen)+1,
		"PromotionStep readyWhen should gain one health condition")

	// The new condition must reference the health Watch node ID
	newCond := stepNode.ReadyWhen[len(stepNode.ReadyWhen)-1]
	assert.Contains(t, newCond, "healthProd", "new readyWhen condition must reference health node ID")
	assert.Contains(t, newCond, "Available", "resource readyWhen checks Available condition")
}

// TestInjectHealthWatchNodes_PropagateWhenUnchanged verifies that propagateWhen
// is NOT modified — the PromotionStep reconciler still gates downstream.
func TestInjectHealthWatchNodes_PropagateWhenUnchanged(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "resource"}},
	})
	g := makeTestGraph("prod")

	var origPropagateWhen []string
	for _, node := range g.Spec.Nodes {
		if node.ID == "prod" {
			origPropagateWhen = append(origPropagateWhen, node.PropagateWhen...)
			break
		}
	}

	_, err := injectHealthWatchNodes(pipeline, g)
	require.NoError(t, err)

	var stepNode *graph.GraphNode
	for i := range g.Spec.Nodes {
		if g.Spec.Nodes[i].ID == "prod" {
			stepNode = &g.Spec.Nodes[i]
			break
		}
	}
	require.NotNil(t, stepNode)

	assert.Equal(t, origPropagateWhen, stepNode.PropagateWhen,
		"propagateWhen must NOT be modified — reconciler still gates downstream")
}

// TestInjectHealthWatchNodes_NilGraph handles nil input gracefully.
func TestInjectHealthWatchNodes_NilGraph(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "resource"}},
	})
	injected, err := injectHealthWatchNodes(pipeline, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, injected, "nil graph must return 0 without error")
}

// TestInjectHealthWatchNodes_NilPipeline handles nil pipeline gracefully.
func TestInjectHealthWatchNodes_NilPipeline(t *testing.T) {
	g := makeTestGraph("prod")
	injected, err := injectHealthWatchNodes(nil, g)
	require.NoError(t, err)
	assert.Equal(t, 0, injected, "nil pipeline must return 0 without error")
}

// TestInjectHealthWatchNodes_UnknownHealthType skips unknown types without error.
func TestInjectHealthWatchNodes_UnknownHealthType(t *testing.T) {
	pipeline := makePipeline("nginx", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "prod", Health: kardinalv1alpha1.HealthConfig{Type: "unknown-type"}},
	})
	g := makeTestGraph("prod")
	originalCount := len(g.Spec.Nodes)

	injected, err := injectHealthWatchNodes(pipeline, g)
	require.NoError(t, err)
	assert.Equal(t, 0, injected, "unknown health type is skipped silently")
	assert.Len(t, g.Spec.Nodes, originalCount, "node count unchanged for unknown type")
}

// TestInjectHealthWatchNodes_WatchNodeIsIdentityOnly verifies that the emitted Watch
// node template conforms to krocodile's ShapeWatch detection requirement:
// only apiVersion, kind, metadata.name/namespace — no spec or other fields.
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

			_, err := injectHealthWatchNodes(pipeline, g)
			require.NoError(t, err)

			watchNode := findHealthNode(g, "prod")
			require.NotNil(t, watchNode, "health Watch node must exist for type %s", tc.healthType)

			tmpl := watchNode.Template

			// Only allowed top-level keys: apiVersion, kind, metadata
			for k := range tmpl {
				assert.Contains(t, []string{"apiVersion", "kind", "metadata"}, k,
					"ShapeWatch template must only have apiVersion/kind/metadata, got: %s", k)
			}

			// metadata must only have name/namespace
			md, ok := tmpl["metadata"].(map[string]interface{})
			require.True(t, ok, "metadata must be a map")
			for k := range md {
				assert.Contains(t, []string{"name", "namespace"}, k,
					"metadata must only have name/namespace for ShapeWatch, got: %s", k)
			}

			// apiVersion and kind must be present
			assert.NotEmpty(t, tmpl["apiVersion"])
			assert.NotEmpty(t, tmpl["kind"])
			assert.NotEmpty(t, md["name"])
			assert.NotEmpty(t, md["namespace"])
		})
	}
}

// TestCelSafeSlug verifies celSafeSlug produces identifiers valid as both
// CEL variable names AND DNS labels (after strings.ToLower), as required by
// krocodile e082fe9+ which embeds node IDs in DNS subdomain label key prefixes.
func TestCelSafeSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Simple names: unchanged
		{"prod", "prod"},
		// Hyphenated: camelCase (hyphens become word boundaries)
		{"prod-eu", "prodEu"},
		{"prod-eu-2", "prodEu2"},
		// All-uppercase: first char lowercased, rest preserved (valid camelCase)
		{"PROD", "pROD"},
		// Leading digit: guarded with "x" prefix (not "_" — underscores invalid in DNS label)
		{"0prod", "x0prod"},
		// Dot-separated: camelCase (dots become word boundaries)
		{"my.env", "myEnv"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := celSafeSlug(tc.input)
			assert.Equal(t, tc.want, got)
			// Invariant: result must be a valid CEL identifier
			assert.Regexp(t, `^[a-zA-Z_][a-zA-Z0-9_]*$`, got, "must be valid CEL identifier")
			// Invariant: lowercase result must be a valid DNS label
			assert.Regexp(t, `^[a-z0-9][a-z0-9]*$`, strings.ToLower(got), "toLower must be valid DNS label")
		})
	}
}

// findHealthNode finds the health Watch node for a given environment in the graph.
func findHealthNode(g *graph.Graph, envName string) *graph.GraphNode {
	target := "health" + strings.ToUpper(celSafeSlug(envName)[:1]) + celSafeSlug(envName)[1:]
	for i := range g.Spec.Nodes {
		if g.Spec.Nodes[i].ID == target {
			return &g.Spec.Nodes[i]
		}
	}
	return nil
}

// TestInjectHealthWatchNodes_ResourceWatchKind verifies that health.type=resource with
// health.labelSelector emits a WatchKind node (no metadata.name, spec.labelSelector present)
// and uses list.all() in the readyWhen CEL expression.
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

	injected, err := injectHealthWatchNodes(pipeline, g)
	require.NoError(t, err)
	assert.Equal(t, 1, injected)

	watchNode := findHealthNode(g, "prod")
	require.NotNil(t, watchNode, "health node for prod must be present")

	tmpl := watchNode.Template
	assert.Equal(t, "apps/v1", tmpl["apiVersion"])
	assert.Equal(t, "Deployment", tmpl["kind"])

	// WatchKind: metadata IS present (with namespace for scoping) but must NOT have metadata.name.
	// krocodile ≥ 81c5a03 changed WatchKind namespace from graph.GetNamespace() to
	// tmpl["metadata"]["namespace"] (absent = cluster-wide list). We include namespace to scope
	// the watch to the environment namespace. The absence of metadata.name is what krocodile uses
	// to classify as ReferenceWatchKind (types.go:DetectReference checks !hasName).
	md, hasMd := tmpl["metadata"].(map[string]interface{})
	require.True(t, hasMd, "WatchKind node template must have metadata block (for namespace scoping)")
	_, hasName := md["name"]
	assert.False(t, hasName, "WatchKind node template must NOT have metadata.name")
	assert.Equal(t, "prod", md["namespace"], "WatchKind must have metadata.namespace = env name")

	// WatchKind: must have top-level "selector" field (krocodile node.go:reconcileWatchKind
	// extracts selector from tmpl["selector"] or tmpl["metadata"]["selector"]).
	labelSelector, ok := tmpl["selector"].(map[string]string)
	require.True(t, ok, "WatchKind node template must have top-level 'selector' of type map[string]string")
	assert.Equal(t, "nginx", labelSelector["app"])
	assert.Equal(t, "nginx", labelSelector["kardinal.io/pipeline"])

	// krocodile >= 05db829: WatchKind nodes use "watch:" keyword and CAN have ReadyWhen.
	// The list.all() predicate is the correct form for collection scope variables.
	require.NotEmpty(t, watchNode.ReadyWhen, "WatchKind node must have ReadyWhen with list.all() predicate")
	assert.Contains(t, watchNode.ReadyWhen[0], ".all(",
		"WatchKind readyWhen must use .all() to check all items")
	assert.NotContains(t, watchNode.ReadyWhen[0], "healthProd.status",
		"WatchKind readyWhen must not use single-object path")
}

// TestInjectHealthWatchNodes_ResourceWatchKindVsWatch verifies that adding a LabelSelector
// switches from Watch to WatchKind without affecting the Watch case for the same health type.
func TestInjectHealthWatchNodes_ResourceWatchKindVsWatch(t *testing.T) {
	// Watch case: no LabelSelector
	watchPipeline := makePipeline("myapp", []kardinalv1alpha1.EnvironmentSpec{
		{Name: "uat", Health: kardinalv1alpha1.HealthConfig{Type: "resource"}},
	})
	gWatch := makeTestGraph("uat")
	_, err := injectHealthWatchNodes(watchPipeline, gWatch)
	require.NoError(t, err)

	watchNode := findHealthNode(gWatch, "uat")
	require.NotNil(t, watchNode)
	tmplWatch := watchNode.Template
	// Watch: must have metadata.name
	md := tmplWatch["metadata"].(map[string]interface{})
	assert.Equal(t, "myapp", md["name"])
	// krocodile >= 81c5a03: Watch/Ref node must NOT have ReadyWhen.
	assert.Empty(t, watchNode.ReadyWhen, "Watch/Ref node must have no ReadyWhen")

	// WatchKind case: with LabelSelector
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
	_, err = injectHealthWatchNodes(watchKindPipeline, gWatchKind)
	require.NoError(t, err)

	watchKindNode := findHealthNode(gWatchKind, "uat")
	require.NotNil(t, watchKindNode)
	tmplWatchKind := watchKindNode.Template
	// WatchKind: metadata IS present (with namespace) but must NOT have metadata.name.
	// krocodile ≥ 81c5a03: WatchKind namespace from tmpl["metadata"]["namespace"], not graph.GetNamespace().
	wkMd, hasMd := tmplWatchKind["metadata"].(map[string]interface{})
	require.True(t, hasMd, "WatchKind node must have metadata block for namespace scoping")
	_, hasName := wkMd["name"]
	assert.False(t, hasName, "WatchKind node must not have metadata.name")
	assert.Equal(t, "uat", wkMd["namespace"], "WatchKind must scope to env namespace")
	// WatchKind: must have top-level selector
	_, hasSelector := tmplWatchKind["selector"]
	assert.True(t, hasSelector, "WatchKind node must have top-level selector")
	assert.Contains(t, watchKindNode.ReadyWhen[0], ".all(",
		"WatchKind readyWhen must use .all() predicate")
}
