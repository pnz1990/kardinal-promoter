// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// supersededHold is the spec.bundleName every PromotionStep node must carry:
// the Bundle name, but only while the Bundle is not Superseded.
const supersededHold = `${[bundle.metadata.name].filter(x_, bundle.status.phase != "Superseded")[0]}`

// TestBuilder_StepsHeldOnceBundleSuperseded verifies that every PromotionStep
// node, including roots and forEach nodes, resolves spec.bundleName only while
// the Bundle is not Superseded (E2E-R20). Without it a Superseded Bundle's
// Graph kept creating steps whenever a gate or upstream turned ready.
func TestBuilder_StepsHeldOnceBundleSuperseded(t *testing.T) {
	gated := makeLinearPipeline("gated", "test", "uat", "prod")
	gate := makePolicyGate("soak", "platform-policies", "prod", "upstream.uat.soakMinutes >= 30")

	cases := []struct {
		name     string
		pipeline *kardinalv1alpha1.Pipeline
		gates    []kardinalv1alpha1.PolicyGate
		steps    int
	}{
		{name: "single root env", pipeline: makeLinearPipeline("one", "test"), steps: 1},
		{name: "linear with prod gate", pipeline: gated, gates: []kardinalv1alpha1.PolicyGate{gate}, steps: 3},
		{
			name: "multi-region forEach",
			pipeline: &kardinalv1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: "fleet", Namespace: "default"},
				Spec: kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{
					{Name: "test"},
					{Name: "prod", Regions: []string{"us-east-1", "eu-west-1"}},
				}},
			},
			steps: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := graph.NewBuilder().Build(graph.BuildInput{
				Pipeline:    tc.pipeline,
				Bundle:      makeBundle(tc.pipeline.Name+"-v1", tc.pipeline.Name),
				PolicyGates: tc.gates,
			})
			require.NoError(t, err)
			assertKroValid(t, result.Graph)

			steps := 0
			for _, n := range result.Graph.Spec.Nodes {
				if n.Template["kind"] != "PromotionStep" {
					continue
				}
				steps++
				spec, ok := n.Template["spec"].(map[string]interface{})
				require.True(t, ok, "node %s has no spec", n.ID)
				assert.Equal(t, supersededHold, spec["bundleName"], "node %s", n.ID)
			}
			assert.Equal(t, tc.steps, steps)
		})
	}
}

// TestBuilder_SupersededHoldIsDataPending evaluates the spec.bundleName
// expression the way kro does. A Superseded Bundle must produce "index out of
// bounds", which kro classifies as data-pending (pkg/graphengine/runtime/
// errors.go celDataPendingPatterns): the node stays Unresolved, is not applied
// and is not pruned. Every other phase, including the recoverable Failed,
// resolves to the Bundle name.
func TestBuilder_SupersededHoldIsDataPending(t *testing.T) {
	env, err := cel.NewEnv(cel.Variable("bundle", cel.DynType))
	require.NoError(t, err)
	expr := strings.TrimSuffix(strings.TrimPrefix(supersededHold, "${"), "}")
	ast, iss := env.Compile(expr)
	require.NoError(t, iss.Err())
	prg, err := env.Program(ast)
	require.NoError(t, err)

	cases := []struct {
		phase   string
		pending bool
	}{
		{phase: "Available"},
		{phase: "Promoting"},
		{phase: "Verified"},
		{phase: "Failed"},
		{phase: "Superseded", pending: true},
	}
	for _, tc := range cases {
		t.Run(tc.phase, func(t *testing.T) {
			out, _, err := prg.Eval(map[string]interface{}{"bundle": map[string]interface{}{
				"metadata": map[string]interface{}{"name": "app-v1"},
				"status":   map[string]interface{}{"phase": tc.phase},
			}})
			if tc.pending {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "index out of bounds")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "app-v1", out.Value())
		})
	}
}
