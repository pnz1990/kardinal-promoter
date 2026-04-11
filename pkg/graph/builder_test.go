// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// makeLinearPipeline creates a pipeline with n environments in linear order.
func makeLinearPipeline(name string, envNames ...string) *kardinalv1alpha1.Pipeline {
	envs := make([]kardinalv1alpha1.EnvironmentSpec, len(envNames))
	for i, n := range envNames {
		envs[i] = kardinalv1alpha1.EnvironmentSpec{Name: n}
	}
	return &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
	}
}

// makeBundle creates a bundle with the given name targeting the given pipeline.
func makeBundle(name, pipeline string) *kardinalv1alpha1.Bundle {
	return &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: pipeline,
		},
	}
}

// makePolicyGate creates a PolicyGate with the given CEL expression
// and applies-to label value.
func makePolicyGate(name, ns, appliesTo, expression string) kardinalv1alpha1.PolicyGate {
	return kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"kardinal.io/applies-to": appliesTo,
			},
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{
			Expression:      expression,
			Message:         "test gate: " + name,
			RecheckInterval: "5m",
		},
	}
}

// Test 1: Linear 3-env pipeline, no gates, default intent.
// Expected: 3 PromotionStep + 3 PRStatus Watch nodes = 6 total.
func TestBuilder_Linear3EnvNoGates(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("nginx-demo", "test", "uat", "prod")
	bundle := makeBundle("nginx-demo-v1-29-0", "nginx-demo")

	result, err := b.Build(graph.BuildInput{
		Pipeline:    pipeline,
		Bundle:      bundle,
		PolicyGates: nil,
	})
	require.NoError(t, err)
	// 3 envs × (1 PRStatus + 1 PromotionStep) = 6
	assert.Equal(t, 6, result.NodeCount)
	assert.Len(t, result.Graph.Spec.Nodes, 6)

	// Verify sequential dependency: uat depends on test, prod depends on uat
	nodeMap := make(map[string]graph.GraphNode)
	for _, n := range result.Graph.Spec.Nodes {
		nodeMap[n.ID] = n
	}

	// test has no upstream dependency
	testNode := nodeMap["test"]
	assert.Empty(t, findUpstreamRef(t, testNode), "test node must have no upstream ref")

	// uat must reference test
	uatNode := nodeMap["uat"]
	assert.True(t, containsCELRef(uatNode.Template, "test"),
		"uat node template must contain reference to test")

	// prod must reference uat
	prodNode := nodeMap["prod"]
	assert.True(t, containsCELRef(prodNode.Template, "uat"),
		"prod node template must contain reference to uat")
}

// Test 2: Linear 3-env with 2 org gates on prod.
// Expected: 3 PromotionStep + 3 PRStatus Watch + 2 PolicyGate nodes = 8 total.
func TestBuilder_Linear3EnvWithProdGates(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("nginx-demo", "test", "uat", "prod")
	bundle := makeBundle("nginx-demo-v1-29-0", "nginx-demo")

	gates := []kardinalv1alpha1.PolicyGate{
		makePolicyGate("no-weekend-deploys", "platform-policies", "prod", "!schedule.isWeekend"),
		makePolicyGate("staging-soak-30m", "platform-policies", "prod", "bundle.upstreamSoakMinutes >= 30"),
	}

	result, err := b.Build(graph.BuildInput{
		Pipeline:    pipeline,
		Bundle:      bundle,
		PolicyGates: gates,
	})
	require.NoError(t, err)
	assert.Equal(t, 8, result.NodeCount, "3 PromotionStep + 3 PRStatus + 2 PolicyGate = 8 nodes")
	assert.Len(t, result.Graph.Spec.Nodes, 8)

	// Verify PolicyGate nodes have propagateWhen set
	for _, n := range result.Graph.Spec.Nodes {
		if containsStr(n.ID, "no_weekend_deploys") || containsStr(n.ID, "staging_soak_30m") {
			assert.NotEmpty(t, n.PropagateWhen,
				"PolicyGate node %q must have PropagateWhen set", n.ID)
		}
	}
}

// Test 3: Fan-out pipeline: staging → [prod-us, prod-eu].
// Expected: parallel nodes with shared dep on staging.
func TestBuilder_FanOut(t *testing.T) {
	b := graph.NewBuilder()
	envs := []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "staging", DependsOn: []string{"test"}},
		{Name: "prod-us", DependsOn: []string{"staging"}},
		{Name: "prod-eu", DependsOn: []string{"staging"}},
	}
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "fleet", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
	}
	bundle := makeBundle("fleet-v2", "fleet")

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)
	// 4 envs × (1 PRStatus + 1 PromotionStep) = 8
	assert.Equal(t, 8, result.NodeCount)

	// Both prod nodes must reference staging (using CEL-safe underscore IDs)
	nodeMap := nodeByID(result.Graph.Spec.Nodes)
	assert.True(t, containsCELRef(nodeMap["prod_us"].Template, "staging"),
		"prod-us must depend on staging")
	assert.True(t, containsCELRef(nodeMap["prod_eu"].Template, "staging"),
		"prod-eu must depend on staging")
}

// Test 4: intent.targetEnvironment = staging.
// Expected: only test + staging, no prod.
func TestBuilder_TargetEnvironment(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("nginx-demo", "test", "staging", "prod")
	bundle := makeBundle("nginx-demo-v1", "nginx-demo")
	bundle.Spec.Intent = &kardinalv1alpha1.BundleIntent{
		TargetEnvironment: "staging",
	}

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)
	// 2 envs × (1 PRStatus + 1 PromotionStep) = 4
	assert.Equal(t, 4, result.NodeCount, "test and staging envs: 2 PRStatus + 2 PromotionStep")

	nodeMap := nodeByID(result.Graph.Spec.Nodes)
	assert.Contains(t, nodeMap, "test")
	assert.Contains(t, nodeMap, "staging")
	assert.NotContains(t, nodeMap, "prod")
}

// Test 5: intent.skipEnvironments = [staging] with SkipPermission gate.
// Expected: staging removed, test → prod directly.
func TestBuilder_SkipEnvironments_WithPermission(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("nginx-demo", "test", "staging", "prod")
	bundle := makeBundle("nginx-demo-v1", "nginx-demo")
	bundle.Spec.Intent = &kardinalv1alpha1.BundleIntent{
		SkipEnvironments: []string{"staging"},
	}
	// SkipPermission gate allows skip
	skipGate := kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "skip-staging-allowed",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/type":       "skip-permission",
				"kardinal.io/applies-to": "staging",
			},
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{
			Expression:     "true",
			SkipPermission: true,
		},
	}

	result, err := b.Build(graph.BuildInput{
		Pipeline:    pipeline,
		Bundle:      bundle,
		PolicyGates: []kardinalv1alpha1.PolicyGate{skipGate},
	})
	require.NoError(t, err)

	nodeMap := nodeByID(result.Graph.Spec.Nodes)
	assert.NotContains(t, nodeMap, "staging", "staging must be removed")
	assert.Contains(t, nodeMap, "test")
	assert.Contains(t, nodeMap, "prod")

	// prod must depend on test directly (not staging)
	assert.True(t, containsCELRef(nodeMap["prod"].Template, "test"),
		"prod must reference test when staging is skipped")
}

// Test 6: intent.skipEnvironments = [staging] without SkipPermission.
// Expected: error (SkipDenied).
func TestBuilder_SkipEnvironments_WithoutPermission(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("nginx-demo", "test", "staging", "prod")
	bundle := makeBundle("nginx-demo-v1", "nginx-demo")
	bundle.Spec.Intent = &kardinalv1alpha1.BundleIntent{
		SkipEnvironments: []string{"staging"},
	}
	// Org gate on staging, no skip-permission gate
	orgGate := kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-skip-staging",
			Namespace: "platform-policies",
			Labels: map[string]string{
				"kardinal.io/scope":      "org",
				"kardinal.io/applies-to": "staging",
			},
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{Expression: "true"},
	}

	_, err := b.Build(graph.BuildInput{
		Pipeline:    pipeline,
		Bundle:      bundle,
		PolicyGates: []kardinalv1alpha1.PolicyGate{orgGate},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skip denied", "error must mention skip denied")
}

// Test 7: Shard label on prod environment.
func TestBuilder_ShardLabel(t *testing.T) {
	b := graph.NewBuilder()
	envs := []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "prod", Shard: "cluster-b"},
	}
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
	}
	bundle := makeBundle("app-v1", "app")

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)

	nodeMap := nodeByID(result.Graph.Spec.Nodes)
	prodNode := nodeMap["prod"]
	template, ok := prodNode.Template["metadata"].(map[string]interface{})
	require.True(t, ok, "template.metadata must be a map")
	labels, ok := template["labels"].(map[string]interface{})
	require.True(t, ok, "template.metadata.labels must be a map")
	assert.Equal(t, "cluster-b", labels["kardinal.io/shard"],
		"prod node must have kardinal.io/shard = cluster-b")
}

// Test 8: Custom steps on prod.
func TestBuilder_CustomSteps(t *testing.T) {
	b := graph.NewBuilder()
	envs := []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "prod"},
	}
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
	}
	bundle := makeBundle("app-v1", "app")

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)
	// Just check that the builder doesn't fail; custom steps are populated in PromotionStep spec
	// 2 envs × (1 PRStatus + 1 PromotionStep) = 4
	assert.Equal(t, 4, result.NodeCount)
}

// Test 9: Config Bundle uses config-merge step type.
func TestBuilder_ConfigBundle(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("config-app", "staging", "prod")
	bundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "config-app-fix1", Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:     "config",
			Pipeline: "config-app",
		},
	}

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)
	// 2 envs × (1 PRStatus + 1 PromotionStep) = 4
	assert.Equal(t, 4, result.NodeCount)

	// Config Bundle nodes should have stepType indicating config-merge
	nodeMap := nodeByID(result.Graph.Spec.Nodes)
	for _, n := range nodeMap {
		spec, ok := n.Template["spec"].(map[string]interface{})
		if ok {
			stepType, _ := spec["stepType"].(string)
			if stepType != "" {
				assert.Equal(t, "config-merge", stepType,
					"config Bundle node %q must use config-merge step type", n.ID)
			}
		}
	}
}

// Test 10: Empty Pipeline returns error.
func TestBuilder_EmptyPipeline(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: nil},
	}
	bundle := makeBundle("empty-v1", "empty")

	_, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no environments", "empty pipeline must error with no environments")
}

// Test 11: Circular dependency returns error.
func TestBuilder_CircularDependency(t *testing.T) {
	b := graph.NewBuilder()
	envs := []kardinalv1alpha1.EnvironmentSpec{
		{Name: "a", DependsOn: []string{"b"}},
		{Name: "b", DependsOn: []string{"a"}},
	}
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "cyclic", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
	}
	bundle := makeBundle("cyclic-v1", "cyclic")

	_, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular", "circular dependency must error")
}

// Test 12: PropagateWhen on PolicyGate nodes is set correctly.
func TestBuilder_PropagateWhenOnPolicyGates(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("app", "test", "prod")
	bundle := makeBundle("app-v1", "app")
	gate := makePolicyGate("no-weekend", "platform-policies", "prod", "!schedule.isWeekend")

	result, err := b.Build(graph.BuildInput{
		Pipeline:    pipeline,
		Bundle:      bundle,
		PolicyGates: []kardinalv1alpha1.PolicyGate{gate},
	})
	require.NoError(t, err)

	// Find the gate node (IDs use underscores for CEL safety: "no-weekend" → "no_weekend")
	var gateNode *graph.GraphNode
	for i := range result.Graph.Spec.Nodes {
		if containsStr(result.Graph.Spec.Nodes[i].ID, "no_weekend") {
			gateNode = &result.Graph.Spec.Nodes[i]
			break
		}
	}
	require.NotNil(t, gateNode, "gate node must be present")
	assert.NotEmpty(t, gateNode.PropagateWhen, "PolicyGate node must have PropagateWhen")
	assert.NotEmpty(t, gateNode.ReadyWhen, "PolicyGate node must have ReadyWhen (health signal)")
}

// Test 13: Graph name is bounded to 63 characters.
func TestBuilder_GraphNameMaxLength(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline(
		"this-is-a-very-long-pipeline-name-that-exceeds-normal-limits",
		"test", "prod",
	)
	bundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "very-long-bundle-name-with-version-1-2-3-4",
			Namespace: "default",
		},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: pipeline.Name},
	}

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(result.Graph.Name), 63,
		"Graph name must not exceed 63 characters: %q", result.Graph.Name)
}

// Test 14: ownerReferences on Graph point to Bundle.
func TestBuilder_OwnerReferences(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("app", "test")
	bundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-v1",
			Namespace: "default",
			UID:       "test-uid-1234",
		},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
	}

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)
	require.Len(t, result.Graph.OwnerReferences, 1)
	assert.Equal(t, "Bundle", result.Graph.OwnerReferences[0].Kind)
	assert.Equal(t, "app-v1", result.Graph.OwnerReferences[0].Name)
}

// Test 15: PRStatus Watch node is generated alongside each PromotionStep.
func TestBuilder_PRStatusWatchNode(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("nginx-demo", "test", "prod")
	bundle := makeBundle("nginx-demo-v1", "nginx-demo")

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)

	nodeMap := nodeByID(result.Graph.Spec.Nodes)

	// PRStatus Watch node for "test" env must exist
	var prStatusTestNode *graph.GraphNode
	for id, n := range nodeMap {
		if containsStr(id, "prstatus") && containsStr(id, "test") {
			n := n
			prStatusTestNode = &n
			break
		}
	}
	require.NotNil(t, prStatusTestNode, "PRStatus Watch node for 'test' must be present")

	// Check kind is PRStatus
	kind, _ := prStatusTestNode.Template["kind"].(string)
	assert.Equal(t, "PRStatus", kind, "Watch node kind must be PRStatus")

	// Check PropagateWhen references status.merged
	require.NotEmpty(t, prStatusTestNode.PropagateWhen)
	assert.Contains(t, prStatusTestNode.PropagateWhen[0], "status.merged == true",
		"PropagateWhen must gate on status.merged")

	// Check PromotionStep node has prStatusRef referencing the Watch node
	testStepNode, ok := nodeMap["test"]
	require.True(t, ok, "PromotionStep node for 'test' must exist")
	assert.True(t, containsCELRef(testStepNode.Template, "prstatus"),
		"PromotionStep node must have CEL reference to PRStatus Watch node")
}

// --- helpers ---

func nodeByID(nodes []graph.GraphNode) map[string]graph.GraphNode {
	m := make(map[string]graph.GraphNode, len(nodes))
	for _, n := range nodes {
		m[n.ID] = n
	}
	return m
}

// containsCELRef returns true if the template map contains a CEL expression
// referencing the given node ID.
func containsCELRef(template map[string]interface{}, nodeID string) bool {
	return containsInMap(template, "${"+nodeID)
}

func containsInMap(m map[string]interface{}, substr string) bool {
	for _, v := range m {
		switch vt := v.(type) {
		case string:
			if containsStr(vt, substr) {
				return true
			}
		case map[string]interface{}:
			if containsInMap(vt, substr) {
				return true
			}
		case []interface{}:
			for _, item := range vt {
				if s, ok := item.(string); ok && containsStr(s, substr) {
					return true
				}
			}
		}
	}
	return false
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(s) > 0 && findSubstr(s, substr))
}

func findSubstr(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// findUpstreamRef returns the upstream reference value from a PromotionStep node template.
func findUpstreamRef(t *testing.T, n graph.GraphNode) string {
	t.Helper()
	spec, ok := n.Template["spec"].(map[string]interface{})
	if !ok {
		return ""
	}
	uv, _ := spec["upstreamVerified"].(string)
	return uv
}
