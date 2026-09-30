// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

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
			Images:   []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v1"}},
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
// Expected: 1 Bundle ref + 3 PromotionStep + 3 PRStatus nodes = 7 total.
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
	assertKroValid(t, result.Graph)
	// 1 Bundle ref + 3 envs × (1 PRStatus + 1 PromotionStep) = 7
	assert.Equal(t, 7, result.NodeCount)
	assert.Len(t, result.Graph.Spec.Nodes, 7)

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
// Expected: 1 Bundle ref + 3 PromotionStep + 3 PRStatus + 2 PolicyGate nodes = 9 total.
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
	assertKroValid(t, result.Graph)
	assert.Equal(t, 9, result.NodeCount, "3 PromotionStep + 3 PRStatus + 2 PolicyGate + 1 Bundle Watch = 9 nodes")
	assert.Len(t, result.Graph.Spec.Nodes, 9)

	// Verify PolicyGate nodes carry a readyWhen health signal and that the
	// prod PromotionStep blocks on each gate via spec.requiredGates.
	nodeMap := nodeByID(result.Graph.Spec.Nodes)
	var gateIDs []string
	for _, n := range result.Graph.Spec.Nodes {
		if containsStr(n.ID, "noWeekendDeploys") || containsStr(n.ID, "stagingSoak30m") {
			gateIDs = append(gateIDs, n.ID)
			require.NotEmpty(t, n.ReadyWhen, "PolicyGate node %q must have ReadyWhen set", n.ID)
			assert.Equal(t, "${"+n.ID+".status.ready == true}", n.ReadyWhen[0])
		}
	}
	require.Len(t, gateIDs, 2)
	prodSpec, _ := nodeMap["prod"].Template["spec"].(map[string]interface{})
	required, _ := prodSpec["requiredGates"].([]interface{})
	require.Len(t, required, 2, "prod must require both gates")
	for _, gid := range gateIDs {
		assert.Contains(t, required,
			"${["+gid+".metadata.name].filter(x_, "+gid+".status.ready == true)[0]}",
			"requiredGates must only resolve once gate %q is ready", gid)
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
	assertKroValid(t, result.Graph)
	// 1 Bundle ref + 4 envs × (1 PRStatus + 1 PromotionStep) = 9
	assert.Equal(t, 9, result.NodeCount)

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
	assertKroValid(t, result.Graph)
	// 1 Bundle ref + 2 envs × (1 PRStatus + 1 PromotionStep) = 5
	assert.Equal(t, 5, result.NodeCount, "test and staging envs: 2 PRStatus + 2 PromotionStep + 1 Bundle Watch")

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
	assertKroValid(t, result.Graph)

	nodeMap := nodeByID(result.Graph.Spec.Nodes)
	// skipEnvironments is filtered statically: kro's includeWhen=false is
	// contagious to dependents, so a runtime exclusion would also drop prod
	// (docs/design/16-graph-capability-ledger.md G2).
	assert.NotContains(t, nodeMap, "staging",
		"staging must be removed from the Graph spec when skipped")
	for _, n := range result.Graph.Spec.Nodes {
		assert.Empty(t, n.IncludeWhen, "node %q must not carry includeWhen", n.ID)
	}
	assert.Contains(t, nodeMap, "test")
	assert.Contains(t, nodeMap, "prod")

	// prod still references its upstreams via upstreamStates
	assert.True(t, containsCELRef(nodeMap["prod"].Template, "staging") ||
		containsCELRef(nodeMap["prod"].Template, "test"),
		"prod must reference at least one upstream")
}

// Test 6: intent.skipEnvironments = [staging] without SkipPermission.
// Build must refuse the skip: an org gate applies to staging and no org
// skip-permission gate allows skipping it. The Bundle reconciler records the
// error as phase Failed with condition Failed/TranslationError.
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

	err := graph.ValidateSkipPermissions(bundle, gates, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skip denied", "error must mention skip denied")

	_, buildErr := b.Build(graph.BuildInput{
		Pipeline:    pipeline,
		Bundle:      bundle,
		PolicyGates: gates,
	})
	require.Error(t, buildErr, "Build must refuse a skip that no permission allows")
	assert.Contains(t, buildErr.Error(), "skip denied")
}

// Test 7: distributed mode was removed (#1321), so a shard no longer labels
// the environment's PromotionSteps: the controller reconciles every step, and
// the step fails with "shard is not supported" instead of being skipped.
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
	assertKroValid(t, result.Graph)

	nodeMap := nodeByID(result.Graph.Spec.Nodes)
	prodNode := nodeMap["prod"]
	template, ok := prodNode.Template["metadata"].(map[string]interface{})
	require.True(t, ok, "template.metadata must be a map")
	labels, ok := template["labels"].(map[string]interface{})
	require.True(t, ok, "template.metadata.labels must be a map")
	assert.NotContains(t, labels, "kardinal.io/shard", "prod node must not carry a shard label")
}

// Test 8: Custom steps are not implemented, so Build rejects them loudly
// instead of silently running the default sequence.
func TestBuilder_CustomSteps(t *testing.T) {
	b := graph.NewBuilder()
	envs := []kardinalv1alpha1.EnvironmentSpec{
		{Name: "test"},
		{Name: "prod", Steps: []kardinalv1alpha1.StepSpec{{Uses: "my-custom-step"}}},
	}
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec:       kardinalv1alpha1.PipelineSpec{Environments: envs},
	}
	bundle := makeBundle("app-v1", "app")

	_, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `environment "prod" declares 1 steps`)
	assert.Contains(t, err.Error(), "not implemented")
}

// Test 9: Config Bundle uses config-merge step type.
func TestBuilder_ConfigBundle(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := makeLinearPipeline("config-app", "staging", "prod")
	bundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "config-app-fix1", Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{
			Type:      "config",
			Pipeline:  "config-app",
			ConfigRef: &kardinalv1alpha1.ConfigRef{GitRepo: "https://github.com/org/config", CommitSHA: "abc123"},
		},
	}

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)
	assertKroValid(t, result.Graph)
	// 1 Bundle ref + 2 envs × (1 PRStatus + 1 PromotionStep) = 5
	assert.Equal(t, 5, result.NodeCount)

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
	// New in #710: error message must show the cycle path
	assert.Contains(t, err.Error(), "→", "error must show cycle path with arrows")
	assert.Contains(t, err.Error(), "Fix:", "error must include fix hint")
}

// Test 12: PolicyGate nodes gate the dependent PromotionStep.
func TestBuilder_PolicyGateGatesDependentStep(t *testing.T) {
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
	assertKroValid(t, result.Graph)

	// Find the gate node (IDs use camelCase: "no-weekend" → "noWeekend")
	var gateNode *graph.GraphNode
	for i := range result.Graph.Spec.Nodes {
		if containsStr(result.Graph.Spec.Nodes[i].ID, "noWeekend") {
			gateNode = &result.Graph.Spec.Nodes[i]
			break
		}
	}
	require.NotNil(t, gateNode, "gate node must be present")
	assert.NotEmpty(t, gateNode.ReadyWhen, "PolicyGate node must have ReadyWhen (health signal)")

	prodSpec, _ := nodeByID(result.Graph.Spec.Nodes)["prod"].Template["spec"].(map[string]interface{})
	required, _ := prodSpec["requiredGates"].([]interface{})
	require.Len(t, required, 1)
	assert.Contains(t, required[0].(string), gateNode.ID+".status.ready == true",
		"prod must not resolve until the gate is ready")
	testSpec, _ := nodeByID(result.Graph.Spec.Nodes)["test"].Template["spec"].(map[string]interface{})
	assert.NotContains(t, testSpec, "requiredGates", "test is not gated")
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
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: pipeline.Name, Images: testImages},
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
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app", Images: testImages},
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
	pipeline.Spec.Environments[0].Approval = "pr-review"
	bundle := makeBundle("nginx-demo-v1", "nginx-demo")

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)
	assertKroValid(t, result.Graph)

	nodeMap := nodeByID(result.Graph.Spec.Nodes)

	prStatusNode := func(env string) *graph.GraphNode {
		for id, n := range nodeMap {
			if containsStr(id, "prstatus") && containsStr(id, env) {
				n := n
				return &n
			}
		}
		return nil
	}

	// PRStatus Watch node for "test" env must exist
	prStatusTestNode := prStatusNode("test")
	require.NotNil(t, prStatusTestNode, "PRStatus Watch node for 'test' must be present")

	// Check kind is PRStatus
	kind, _ := prStatusTestNode.Template["kind"].(string)
	assert.Equal(t, "PRStatus", kind, "Watch node kind must be PRStatus")

	// The PRStatus template carries no spec: the SCM step fills it in later
	// and kro's SSA must not own (and revert) those fields.
	assert.NotContains(t, prStatusTestNode.Template, "spec",
		"PRStatus template must not carry a spec")

	// Check ReadyWhen references status.merged (health signal for UI only)
	require.NotEmpty(t, prStatusTestNode.ReadyWhen)
	assert.Contains(t, prStatusTestNode.ReadyWhen[0], "status.merged == true",
		"ReadyWhen must gate on status.merged for UI health display")

	// An auto environment never opens a PR, so a merged readyWhen would keep
	// the Graph from ever reaching Ready.
	prStatusProdNode := prStatusNode("prod")
	require.NotNil(t, prStatusProdNode, "PRStatus Watch node for 'prod' must be present")
	assert.Empty(t, prStatusProdNode.ReadyWhen, "auto environments must not carry a PRStatus readyWhen")

	// Check PromotionStep node has prStatusRef referencing the Watch node
	testStepNode, ok := nodeMap["test"]
	require.True(t, ok, "PromotionStep node for 'test' must exist")
	assert.True(t, containsCELRef(testStepNode.Template, prStatusTestNode.ID),
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

// containsCELRef reports whether any ${...} expression in template references
// the node nodeID (exactly: a reference to "prodEu" does not count as one to
// "prod"). The reference is what creates the Graph dependency edge, wherever
// it sits inside the expression.
func containsCELRef(template map[string]interface{}, nodeID string) bool {
	re := regexp.MustCompile(`(?:\$\{|[\s(\[,!&|])` + regexp.QuoteMeta(nodeID) + `\.`)
	return containsInMapFunc(template, func(s string) bool {
		return strings.Contains(s, "${") && re.MatchString(s)
	})
}

func containsInMapFunc(m map[string]interface{}, match func(string) bool) bool {
	for _, v := range m {
		switch vt := v.(type) {
		case string:
			if match(vt) {
				return true
			}
		case map[string]interface{}:
			if containsInMapFunc(vt, match) {
				return true
			}
		case []interface{}:
			for _, item := range vt {
				if s, ok := item.(string); ok && match(s) {
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
	assertKroValid(t, result.Graph)

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

// TestBuilder_WaveTopology_2Wave_Plus_Serial verifies that a wave listed
// after sequential environments waits for the last of them: "prod-1 and
// prod-2 start together after staging" (C01-graph-01).
func TestBuilder_WaveTopology_2Wave_Plus_Serial(t *testing.T) {
	b := graph.NewBuilder()
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "mixed-pipe", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},            // no wave — sequential root
				{Name: "staging"},         // no wave — sequential: depends on test
				{Name: "prod-1", Wave: 1}, // wave 1 — follows staging
				{Name: "prod-2", Wave: 1}, // wave 1 — follows staging, parallel to prod-1
			},
		},
	}
	bundle := makeBundle("app-v2", "mixed-pipe")

	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle})
	require.NoError(t, err)
	assertKroValid(t, result.Graph)

	nodeMap := nodeByID(result.Graph.Spec.Nodes)
	assert.Equal(t, []string{"test"}, upstreamIDs(t, nodeMap, "staging"))
	assert.Equal(t, []string{"staging"}, upstreamIDs(t, nodeMap, "prod1"))
	assert.Equal(t, []string{"staging"}, upstreamIDs(t, nodeMap, "prod2"))
}

// TestBuilder_WaveTopology_Ordering covers the wave rules: a wave waits for
// every environment of the previous wave and for the sequential environment
// listed before it; gaps in wave numbers do not create roots; only the first
// environment is a root (C01-graph-01, C12-examples-demo-02, E2E-21).
func TestBuilder_WaveTopology_Ordering(t *testing.T) {
	tests := []struct {
		name string
		envs []kardinalv1alpha1.EnvironmentSpec
		want map[string][]string // node ID → upstream node IDs
	}{
		{
			name: "wave-topology example",
			envs: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"}, {Name: "staging"},
				{Name: "prod-eu", Wave: 1}, {Name: "prod-us", Wave: 1}, {Name: "prod-ap", Wave: 2},
			},
			want: map[string][]string{
				"test": nil, "staging": {"test"},
				"prodEu": {"staging"}, "prodUs": {"staging"}, "prodAp": {"prodEu", "prodUs"},
			},
		},
		{
			name: "wave numbers with gaps",
			envs: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test", Wave: 10}, {Name: "staging", Wave: 20}, {Name: "prod", Wave: 30},
			},
			want: map[string][]string{"test": nil, "staging": {"test"}, "prod": {"staging"}},
		},
		{
			name: "sequential environment between waves",
			envs: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "eu", Wave: 1}, {Name: "us", Wave: 1},
				{Name: "soak"},
				{Name: "global", Wave: 2},
			},
			want: map[string][]string{
				"eu": nil, "us": nil, "soak": {"eu", "us"}, "global": {"eu", "soak", "us"},
			},
		},
		{
			name: "wave listed before sequential environments",
			envs: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "canary", Wave: 1}, {Name: "prod"},
			},
			want: map[string][]string{"canary": nil, "prod": {"canary"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pipeline := &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "waves", Namespace: "default"},
				Spec:       kardinalv1alpha1.PipelineSpec{Environments: tt.envs},
			}
			result, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: pipeline, Bundle: makeBundle("waves-v1", "waves")})
			require.NoError(t, err)
			assertKroValid(t, result.Graph)
			nodeMap := nodeByID(result.Graph.Spec.Nodes)
			for id, want := range tt.want {
				assert.Equal(t, want, upstreamIDs(t, nodeMap, id), "upstreams of %s", id)
			}
		})
	}
}

// upstreamIDs returns the sorted node IDs a PromotionStep node's
// spec.upstreamStates references, or nil for a root.
func upstreamIDs(t *testing.T, nodes map[string]graph.GraphNode, id string) []string {
	t.Helper()
	n, ok := nodes[id]
	require.True(t, ok, "node %q must exist", id)
	re := regexp.MustCompile(`filter\(x_, (?:size\()?([A-Za-z][A-Za-z0-9]*)[.)]`)
	var ids []string
	for _, ref := range findUpstreamRefs(t, n) {
		s, _ := ref.(string)
		m := re.FindStringSubmatch(s)
		require.NotNil(t, m, "unexpected upstreamStates entry %q", s)
		ids = append(ids, m[1])
	}
	sort.Strings(ids)
	return ids
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
	assertKroValid(t, result.Graph)

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
	uatNode, ok := nodeMap["uat"]
	require.True(t, ok, "uat node must exist")
	assert.True(t, containsCELRef(uatNode.Template, "test"),
		"uat must depend on test in sequential (no-wave) pipeline")

	prodNode, ok := nodeMap["prod"]
	require.True(t, ok, "prod node must exist")
	assert.True(t, containsCELRef(prodNode.Template, "uat"),
		"prod must depend on uat in sequential (no-wave) pipeline")
}

// ---------------------------------------------------------------------------
// Node ID invariant tests — kro validates every node ID against
// ^[A-Za-z][A-Za-z0-9]*$ and rejects a reserved vocabulary
// (pkg/graphengine/compiler/validation.go). IDs have no length limit.
// ---------------------------------------------------------------------------

// reKroNodeID is kro's node ID grammar.
var reKroNodeID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

// kroReservedNodeIDs mirrors kro's reservedNodeIDs set.
var kroReservedNodeIDs = map[string]bool{
	"apiVersion": true, "kind": true, "metadata": true, "namespace": true, "spec": true, "status": true,
	"graph": true, "graphengine": true, "kro": true,
	"each": true, "item": true, "items": true, "object": true, "self": true, "this": true, "context": true,
	"true": true, "false": true, "null": true, "in": true, "as": true, "break": true, "const": true,
	"continue": true, "else": true, "for": true, "function": true, "if": true, "import": true, "let": true,
	"loop": true, "package": true, "return": true, "var": true, "void": true, "while": true,
}

// assertKroValid checks a built Graph the way kro and the API server will,
// independently of graph.ValidateNodeIDs: every node ID matches kro's grammar,
// is not reserved, is unique and is not a forEach iterator name; every
// template renders a unique, valid metadata.name; every literal label value
// is valid; and the Graph name and labels are valid label values.
func assertKroValid(t *testing.T, g *graph.Graph) {
	t.Helper()
	assert.Empty(t, validation.IsValidLabelValue(g.Name), "Graph name %q", g.Name)
	for k, v := range g.Labels {
		assert.Empty(t, validation.IsValidLabelValue(v), "Graph label %s=%q", k, v)
	}
	iterators := map[string]bool{}
	for _, n := range g.Spec.Nodes {
		for _, dim := range n.ForEach {
			for name := range dim {
				iterators[name] = true
			}
		}
	}
	seen := map[string]bool{}
	names := map[string]string{}
	for _, n := range g.Spec.Nodes {
		id := n.ID
		assert.True(t, reKroNodeID.MatchString(id),
			"node ID %q does not match kro's node ID grammar ^[A-Za-z][A-Za-z0-9]*$", id)
		assert.False(t, kroReservedNodeIDs[id], "node ID %q is reserved by kro", id)
		assert.False(t, seen[id], "duplicate node ID %q", id)
		assert.False(t, iterators[id], "node ID %q is a forEach iterator name", id)
		seen[id] = true
		if n.Template == nil {
			continue
		}
		md, ok := n.Template["metadata"].(map[string]interface{})
		require.True(t, ok, "node %s has no template metadata", id)
		name, _ := md["name"].(string)
		key := fmt.Sprint(n.Template["kind"]) + "/" + name
		if prev, dup := names[key]; dup {
			t.Errorf("nodes %s and %s both render %s", prev, id, key)
		}
		names[key] = id
		// A forEach name ends in "-${region}"; a region is a DNS-1123 label.
		rendered := strings.ReplaceAll(name, "${region}", "us-east-1")
		assert.Empty(t, validation.IsDNS1123Subdomain(rendered), "node %s metadata.name %q", id, name)
		labels, _ := md["labels"].(map[string]interface{})
		for k, v := range labels {
			if s, ok := v.(string); ok && !strings.Contains(s, "${") {
				assert.Empty(t, validation.IsValidLabelValue(s), "node %s label %s=%q", id, k, s)
			}
		}
	}
}

// TestNodeIDs_KroValid verifies that all node IDs emitted by the builder for
// typical real-world env names pass kro's node ID validation.
func TestNodeIDs_KroValid(t *testing.T) {
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
			assertKroValid(t, result.Graph)
		})
	}
}

// TestNodeIDs_LongGateNodeIDsKept verifies that long composite gate node IDs
// are emitted in full: kro imposes no length limit on node IDs, so the IDs
// stay readable and collision-free.
func TestNodeIDs_LongGateNodeIDsKept(t *testing.T) {
	// Long names in all components: gate name, namespace, env name, bundle.
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
	gates := []kardinalv1alpha1.PolicyGate{
		makePolicyGate("no-weekend-deploys", "platform-policies", "kardinal-test-app-prod", "true"),
		makePolicyGate("require-uat-soak-30m", "platform-policies", "kardinal-test-app-prod", "true"),
	}
	b := graph.NewBuilder()
	result, err := b.Build(graph.BuildInput{Pipeline: pipeline, Bundle: bundle, PolicyGates: gates})
	require.NoError(t, err)
	assertKroValid(t, result.Graph)

	nodeMap := nodeByID(result.Graph.Spec.Nodes)
	var gateIDs []string
	for id := range nodeMap {
		if strings.HasPrefix(id, "noWeekendDeploys0") || strings.HasPrefix(id, "requireUatSoak30m0") {
			gateIDs = append(gateIDs, id)
		}
	}
	require.Len(t, gateIDs, 2, "both gates must be emitted")
	for _, id := range gateIDs {
		assert.Greater(t, len(id), 63, "gate node ID %q must not be truncated", id)
		assert.Contains(t, id, "0platformPolicies0kardinalTestAppProd00")
	}
}

// TestBuilder_RejectsCustomStepsAndTemplates verifies that Build refuses
// spec.environments[].steps and promotionTemplate. The PromotionStep
// reconciler always runs the default sequence, so accepting them would
// silently skip the steps the author declared (C01-graph-27).
func TestBuilder_RejectsCustomStepsAndTemplates(t *testing.T) {
	steps := []kardinalv1alpha1.StepSpec{
		{Uses: "git-clone"},
		{Uses: "notify-slack", Webhook: &kardinalv1alpha1.WebhookConfig{URL: "https://hooks.example.com"}},
	}
	tests := []struct {
		name    string
		env     kardinalv1alpha1.EnvironmentSpec
		wantErr string
	}{
		{
			name:    "steps",
			env:     kardinalv1alpha1.EnvironmentSpec{Name: "prod", Steps: steps},
			wantErr: `environment "prod" declares 2 steps`,
		},
		{
			name: "promotion template",
			env: kardinalv1alpha1.EnvironmentSpec{Name: "prod",
				PromotionTemplate: &kardinalv1alpha1.PromotionTemplateRef{Name: "standard"}},
			wantErr: `environment "prod" references PromotionTemplate "standard"`,
		},
		{
			name: "both",
			env: kardinalv1alpha1.EnvironmentSpec{Name: "prod", Steps: steps,
				PromotionTemplate: &kardinalv1alpha1.PromotionTemplateRef{Name: "standard"}},
			wantErr: `environment "prod" declares 2 steps`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pipeline := &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
				Spec: kardinalv1alpha1.PipelineSpec{
					Git:          kardinalv1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
					Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, tt.env},
				},
			}
			_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: pipeline, Bundle: makeBundle("app-v1", "app")})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Contains(t, err.Error(), "not implemented")
		})
	}
}

// TestBuilder_MultiRegionFanOut verifies #1304: two or more regions fail at
// Graph build with a message that points to one environment per region. The
// builder used to emit a forEach node whose region steps then failed at run
// time, because every region pushed the same change to the same branch.
func TestBuilder_MultiRegionFanOut(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "fleet", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test"},
				{Name: "prod", Regions: []string{"us-east-1", "eu-west-1"}},
			},
		},
	}
	_, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: pipeline, Bundle: makeBundle("fleet-v1", "fleet")})
	require.Error(t, err)
	assert.ErrorIs(t, err, graph.ErrInvalid)
	assert.Contains(t, err.Error(), `environment "prod": regions is not supported; declare one environment `+
		`per region (prod-us, prod-eu) and use wave`)
}

// TestBuilder_SingleRegionNoForEach verifies that one region (Regions: ["x"])
// is accepted and ignored: no forEach node and no spec.region (issues #1111,
// #1304).
func TestBuilder_SingleRegionNoForEach(t *testing.T) {
	b := graph.NewBuilder()

	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "single-fleet", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{
					Name:    "prod",
					Regions: []string{"us-east-1"}, // exactly one region — must NOT produce forEach
				},
			},
		},
	}
	bundle := makeBundle("single-fleet-v1", "single-fleet")

	result, err := b.Build(graph.BuildInput{
		Pipeline:    pipeline,
		Bundle:      bundle,
		PolicyGates: nil,
	})
	require.NoError(t, err)
	assertKroValid(t, result.Graph)

	nodeMap := nodeByID(result.Graph.Spec.Nodes)

	prodNode := nodeMap["prod"]
	require.NotNil(t, prodNode, "prod node must exist")

	// ForEach must be empty for a single-region environment
	assert.Empty(t, prodNode.ForEach, "single-region environment (regions=[x]) must not have ForEach set")

	// spec.region must NOT be set in the PromotionStep template
	prodSpec, ok := prodNode.Template["spec"].(map[string]interface{})
	require.True(t, ok, "prod node template must have spec")
	_, hasRegion := prodSpec["region"]
	assert.False(t, hasRegion, "single-region environment (regions=[x]) must not have spec.region in template")
}

// testImages is the image of the test Bundles: an image Bundle needs one.
var testImages = []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v1"}}
