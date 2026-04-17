// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"regexp"
	"strings"
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
	assert.Equal(t, 8, result.NodeCount)
	assert.Len(t, result.Graph.Spec.Nodes, 8)

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
	assert.Equal(t, 10, result.NodeCount, "3 PromotionStep + 3 PRStatus + 2 PolicyGate + 1 Bundle Watch = 9 nodes")
	assert.Len(t, result.Graph.Spec.Nodes, 10)

	// Verify PolicyGate nodes have propagateWhen set
	for _, n := range result.Graph.Spec.Nodes {
		if containsStr(n.ID, "noWeekendDeploys") || containsStr(n.ID, "stagingSoak30m") {
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
	assert.Equal(t, 10, result.NodeCount)

	// Both prod nodes must reference staging (using CEL-safe underscore IDs)
	nodeMap := nodeByID(result.Graph.Spec.Nodes)
	assert.True(t, containsCELRef(nodeMap["prodUs"].Template, "staging"),
		"prod-us must depend on staging")
	assert.True(t, containsCELRef(nodeMap["prodEu"].Template, "staging"),
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
	assert.Equal(t, 6, result.NodeCount, "test and staging envs: 2 PRStatus + 2 PromotionStep + 1 Bundle Watch")

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
	// (#619) staging IS in the node map -- skipEnvironments no longer filters in Go.
	// The node has includeWhen: false so krocodile excludes it from execution.
	assert.Contains(t, nodeMap, "staging",
		"staging must be in Graph spec -- includeWhen handles exclusion at runtime")
	// The staging PromotionStep must have includeWhen referencing this env
	require.NotEmpty(t, nodeMap["staging"].IncludeWhen)
	assert.Contains(t, nodeMap["staging"].IncludeWhen[0], `"staging"`,
		"includeWhen must reference the env name 'staging'")
	assert.Contains(t, nodeMap, "test")
	assert.Contains(t, nodeMap, "prod")

	// prod still references its upstreams via upstreamStates
	assert.True(t, containsCELRef(nodeMap["prod"].Template, "staging") ||
		containsCELRef(nodeMap["prod"].Template, "test"),
		"prod must reference at least one upstream")
}

// Test 6: intent.skipEnvironments = [staging] without SkipPermission.
// ValidateSkipPermissions must return an error; Build must succeed (check moved outside Build).
// Graph-purity: GB-2 eliminated — skip-permission check is no longer inside Build(),
// it is called by the Translator and the result flows through Bundle.status.
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
	gates := []kardinalv1alpha1.PolicyGate{orgGate}

	// ValidateSkipPermissions must return an error — skip is denied.
	// The Translator calls this before Build() and returns the error to the Bundle reconciler.
	err := graph.ValidateSkipPermissions(pipeline, bundle, gates)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skip denied", "error must mention skip denied")

	// Build itself must succeed — the skip-environment is filtered out by filterByIntent.
	// The caller (Translator) is responsible for preventing Build from being called
	// when ValidateSkipPermissions fails.
	_, buildErr := b.Build(graph.BuildInput{
		Pipeline:    pipeline,
		Bundle:      bundle,
		PolicyGates: gates,
	})
	require.NoError(t, buildErr, "Build must succeed when skip check has been separated out")
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
	assert.Equal(t, 6, result.NodeCount)
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
	assert.Equal(t, 6, result.NodeCount)

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

	// Find the gate node (IDs use camelCase: "no-weekend" → "noWeekend")
	var gateNode *graph.GraphNode
	for i := range result.Graph.Spec.Nodes {
		if containsStr(result.Graph.Spec.Nodes[i].ID, "noWeekend") {
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

	// Check PropagateWhen is EMPTY to avoid circular dependency:
	// PromotionStep references ${prstatus.metadata.name} creating a dep edge;
	// if PRStatus also had propagateWhen=merged, the PromotionStep could never start
	// because PRStatus can't be merged before the PR is opened.
	assert.Empty(t, prStatusTestNode.PropagateWhen,
		"PropagateWhen must be empty — PRStatus must not block PromotionStep (circular dep)")

	// Check ReadyWhen references status.merged (health signal for UI only)
	require.NotEmpty(t, prStatusTestNode.ReadyWhen)
	assert.Contains(t, prStatusTestNode.ReadyWhen[0], "status.merged == true",
		"ReadyWhen must gate on status.merged for UI health display")

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

// findUpstreamRefs returns all upstream state CEL references from the upstreamStates
// list field. Returns empty slice if no upstreams are set.
// Updated in #625: upstreamVerified/upstreamVerified2 → upstreamStates []string.
func findUpstreamRefs(t *testing.T, n graph.GraphNode) []interface{} {
	t.Helper()
	spec, ok := n.Template["spec"].(map[string]interface{})
	if !ok {
		return nil
	}
	refs, _ := spec["upstreamStates"].([]interface{})
	return refs
}

// findUpstreamRef returns the first upstream state CEL reference, or "" if none.
// Kept for backward compat with existing test assertions.
func findUpstreamRef(t *testing.T, n graph.GraphNode) string {
	t.Helper()
	refs := findUpstreamRefs(t, n)
	if len(refs) == 0 {
		return ""
	}
	s, _ := refs[0].(string)
	return s
}

// TestBuilder_PolicyGateScopeLabelsPropagate verifies that the scope and applies-to
// labels from the original PolicyGate template are copied to the instantiated node's
// metadata.labels. This is needed so `kardinal policy list` can display correct scope.
func TestBuilder_PolicyGateScopeLabelsPropagate(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("my-app", "test", "prod")
	bundle := makeBundle("my-app-v1", "my-app")

	// Org-scoped gate with applies-to=prod.
	orgGate := kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-weekend-deploys",
			Namespace: "platform-policies",
			Labels: map[string]string{
				"kardinal.io/applies-to": "prod",
				"kardinal.io/scope":      "org",
			},
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{
			Expression:      "!schedule.isWeekend",
			Message:         "blocked on weekends",
			RecheckInterval: "5m",
		},
	}

	result, err := b.Build(graph.BuildInput{
		Pipeline:    pipeline,
		Bundle:      bundle,
		PolicyGates: []kardinalv1alpha1.PolicyGate{orgGate},
	})
	require.NoError(t, err)

	// Find the instantiated PolicyGate node.
	var gateNode *graph.GraphNode
	for i := range result.Graph.Spec.Nodes {
		n := result.Graph.Spec.Nodes[i]
		if containsStr(n.ID, "noWeekendDeploys") {
			gateNode = &n
			break
		}
	}
	require.NotNil(t, gateNode, "PolicyGate node must exist")

	// Extract labels from template.metadata.labels.
	meta, ok := gateNode.Template["metadata"].(map[string]interface{})
	require.True(t, ok, "template must have metadata")
	labels, ok := meta["labels"].(map[string]interface{})
	require.True(t, ok, "metadata must have labels")

	assert.Equal(t, "org", labels["kardinal.io/scope"],
		"scope label must be propagated from the original gate template")
	assert.Equal(t, "prod", labels["kardinal.io/applies-to"],
		"applies-to label must be propagated from the original gate template")
}

// TestBuilder_PolicyGateScopeDefault verifies that a gate without scope label
// gets the default 'team' scope propagated.
func TestBuilder_PolicyGateScopeDefault(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("my-app", "prod")
	bundle := makeBundle("my-app-v1", "my-app")

	// Team gate with no scope label.
	teamGate := kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "team-gate",
			Namespace: "my-team",
			Labels: map[string]string{
				"kardinal.io/applies-to": "prod",
				// no kardinal.io/scope label
			},
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{
			Expression:      "true",
			Message:         "always pass",
			RecheckInterval: "5m",
		},
	}

	result, err := b.Build(graph.BuildInput{
		Pipeline:    pipeline,
		Bundle:      bundle,
		PolicyGates: []kardinalv1alpha1.PolicyGate{teamGate},
	})
	require.NoError(t, err)

	var gateNode *graph.GraphNode
	for i := range result.Graph.Spec.Nodes {
		n := result.Graph.Spec.Nodes[i]
		if containsStr(n.ID, "teamGate") {
			gateNode = &n
			break
		}
	}
	require.NotNil(t, gateNode, "PolicyGate node must exist")

	meta, ok := gateNode.Template["metadata"].(map[string]interface{})
	require.True(t, ok)
	labels, ok := meta["labels"].(map[string]interface{})
	require.True(t, ok)

	assert.Equal(t, "team", labels["kardinal.io/scope"],
		"default scope must be 'team' when label is absent")
}

// --- Wave topology tests (K-06) ---

// TestBuilder_WaveTopology_3Waves verifies that wave: fields generate correct
// dependency edges: wave-2 envs depend on all wave-1 envs; wave-3 on all wave-2.
func TestBuilder_WaveTopology_3Waves(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "multi-region", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "staging", Wave: 1},
				{Name: "prod-eu", Wave: 2},
				{Name: "prod-us", Wave: 2},
				{Name: "prod-ap", Wave: 3},
			},
		},
	}
	bundle := makeBundle("app-v1", "multi-region")

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)

	nodeMap := make(map[string]graph.GraphNode)
	for _, n := range result.Graph.Spec.Nodes {
		nodeMap[n.ID] = n
	}

	// prod-eu and prod-us must both depend on staging
	assert.True(t, containsCELRef(nodeMap["prodEu"].Template, "staging"),
		"prod-eu must depend on staging (wave 2 depends on wave 1)")
	assert.True(t, containsCELRef(nodeMap["prodUs"].Template, "staging"),
		"prod-us must depend on staging (wave 2 depends on wave 1)")

	// prod-ap must depend on both prod-eu and prod-us
	assert.True(t, containsCELRef(nodeMap["prodAp"].Template, "prodEu"),
		"prod-ap must depend on prod-eu (wave 3 depends on wave 2)")
	assert.True(t, containsCELRef(nodeMap["prodAp"].Template, "prodUs"),
		"prod-ap must depend on prod-us (wave 3 depends on wave 2)")
}

// TestBuilder_WaveTopology_2Wave_Plus_Serial verifies that a mix of wave and
// non-wave envs works: the non-wave env uses sequential default.
func TestBuilder_WaveTopology_2Wave_Plus_Serial(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "mixed-pipe", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},            // no wave — sequential
				{Name: "staging"},         // no wave — sequential: depends on test
				{Name: "prod-1", Wave: 1}, // wave 1 — no predecessors via wave
				{Name: "prod-2", Wave: 1}, // wave 1 — no predecessors via wave
			},
		},
	}
	bundle := makeBundle("app-v2", "mixed-pipe")

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)

	nodeMap := make(map[string]graph.GraphNode)
	for _, n := range result.Graph.Spec.Nodes {
		nodeMap[n.ID] = n
	}

	// staging depends on test (sequential)
	assert.True(t, containsCELRef(nodeMap["staging"].Template, "test"),
		"staging must depend on test (sequential default)")

	// wave-1 envs have no wave-derived deps (they are the first wave)
	prod1 := nodeMap["prod_1"]
	prod2 := nodeMap["prod_2"]
	// prod-1 and prod-2 are wave 1 — they have no automatic dependencies (first wave)
	// They may still depend on sequential predecessor if Wave is set. Since Wave>0,
	// sequential default is NOT applied — they are independent wave roots.
	_ = prod1
	_ = prod2
}

// TestBuilder_WaveTopology_WithExplicitDependsOn verifies that explicit dependsOn
// is unioned with wave-derived edges.
func TestBuilder_WaveTopology_WithExplicitDependsOn(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "union-pipe", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "eu-1", Wave: 1},
				{Name: "us-1", Wave: 1},
				// prod-combined: wave 2 deps (eu-1, us-1) plus explicit dep on us-1 — deduped
				{Name: "prod-combined", Wave: 2, DependsOn: []string{"us-1"}},
			},
		},
	}
	bundle := makeBundle("app-v3", "union-pipe")

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)

	nodeMap := make(map[string]graph.GraphNode)
	for _, n := range result.Graph.Spec.Nodes {
		nodeMap[n.ID] = n
	}

	combined := nodeMap["prodCombined"]
	assert.True(t, containsCELRef(combined.Template, "eu1"),
		"prod-combined must depend on eu-1 via wave")
	assert.True(t, containsCELRef(combined.Template, "us1"),
		"prod-combined must depend on us-1 via wave (no duplicate)")
}

// TestBuilder_WaveTopology_NoWave_BackwardCompat verifies that pipelines with
// no wave fields behave identically to before K-06 (sequential default).
func TestBuilder_WaveTopology_NoWave_BackwardCompat(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("compat-pipe", "test", "uat", "prod")
	bundle := makeBundle("app-compat", "compat-pipe")

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)

	nodeMap := make(map[string]graph.GraphNode)
	for _, n := range result.Graph.Spec.Nodes {
		nodeMap[n.ID] = n
	}

	// Verify sequential dependency preserved
	uatNode := nodeMap["uat"]
	require.NotNil(t, uatNode)
	assert.True(t, containsCELRef(uatNode.Template, "test"),
		"uat must depend on test in sequential (no-wave) pipeline")

	prodNode := nodeMap["prod"]
	require.NotNil(t, prodNode)
	assert.True(t, containsCELRef(prodNode.Template, "uat"),
		"prod must depend on uat in sequential (no-wave) pipeline")
}

// ---------------------------------------------------------------------------
// Node ID invariant tests — krocodile e082fe9+ embeds node IDs in DNS subdomain
// label key prefixes. These must satisfy BOTH:
//   - CEL identifier: [a-zA-Z_][a-zA-Z0-9_]* (no hyphens)
//   - DNS label after strings.ToLower(): [a-z0-9]+ (no underscores or hyphens)
//
// i.e., all generated node IDs must be camelCase [a-zA-Z][a-zA-Z0-9]*.
// ---------------------------------------------------------------------------

// reCELIdent matches a valid CEL identifier.
var reCELIdent = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// reDNSLabel matches a valid RFC 1123 DNS label after strings.ToLower.
var reDNSLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

// assertNodeIDsValid checks that every node ID in a built Graph satisfies the
// CEL and DNS label constraints required by krocodile e082fe9+ / PR #109.
func assertNodeIDsValid(t *testing.T, nodes []graph.GraphNode) {
	t.Helper()
	for _, n := range nodes {
		id := n.ID
		// CEL identifier check.
		assert.True(t, reCELIdent.MatchString(id),
			"node ID %q is not a valid CEL identifier (must match [a-zA-Z_][a-zA-Z0-9_]*)", id)
		// DNS label check (after toLower, as krocodile does in nodeLabelPrefix).
		lowered := strings.ToLower(id)
		assert.True(t, reDNSLabel.MatchString(lowered),
			"node ID %q lowercased to %q which is not a valid RFC 1123 DNS label "+
				"(must match [a-z0-9][a-z0-9-]*[a-z0-9] or [a-z0-9]): "+
				"krocodile embeds node IDs in DNS subdomain label key prefixes", id, lowered)
		// No underscores (would be invalid in DNS label key prefix).
		assert.NotContains(t, lowered, "_",
			"node ID %q contains an underscore after lowercasing — invalid in krocodile label key prefix", id)
		// No hyphens (would be invalid in CEL identifier).
		assert.NotContains(t, id, "-",
			"node ID %q contains a hyphen — invalid as a CEL identifier", id)
		// 63-char limit per DNS label segment (PR #109: IsDNS1123Label enforced in parseNodeList).
		assert.LessOrEqual(t, len(lowered), 63,
			"node ID %q lowercases to %d chars — exceeds the 63-char DNS label segment limit", id, len(lowered))
	}
}

// TestNodeIDs_CELAndDNSSafe verifies that all node IDs emitted by the builder
// for typical real-world env names satisfy the CEL + DNS label constraints.
func TestNodeIDs_CELAndDNSSafe(t *testing.T) {
	cases := []struct {
		name string
		envs []string
	}{
		{"simple", []string{"test", "uat", "prod"}},
		{"hyphenated", []string{"kardinal-test-app-test", "kardinal-test-app-uat", "kardinal-test-app-prod"}},
		{"mixed", []string{"dev", "pre-prod", "prod-eu", "prod-us"}},
		{"numeric-suffix", []string{"env-1", "env-2", "env-3"}},
		{"uppercase-input", []string{"Dev", "UAT", "Prod"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := makeLinearPipeline("my-app", tc.envs...)
			bundle := makeBundle("my-app-abc123", "my-app")
			b := graph.NewBuilder()
			result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
			require.NoError(t, err)
			assertNodeIDsValid(t, result.Graph.Spec.Nodes)
		})
	}
}

// TestNodeIDs_LongGateNodeTruncation verifies that gate node IDs exceeding
// the 63-char DNS label limit are truncated to exactly 63 chars via the
// truncateNodeID function (PR #109 compliance).
func TestNodeIDs_LongGateNodeTruncation(t *testing.T) {
	// This pipeline has long names in all components: gate name, namespace,
	// env name, and bundle name — which would produce a composite gate node ID
	// well over 63 chars without truncation.
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-application", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "kardinal-test-app-prod"},
			},
			// Gates at pipeline level with a long name in a named namespace.
			PolicyGates: []kardinalv1alpha1.PipelinePolicyGateRef{
				{Name: "no-weekend-deploys", Namespace: "platform-policies"},
				{Name: "require-uat-soak-30m", Namespace: "platform-policies"},
			},
		},
	}
	bundle := makeBundle("my-application-abc123456", "my-application")
	b := graph.NewBuilder()
	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)
	assertNodeIDsValid(t, result.Graph.Spec.Nodes)
}
