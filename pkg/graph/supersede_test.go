// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// supersededHold is the spec.bundleName every PromotionStep node must carry:
// the Bundle name, but only while the Bundle is neither Superseded nor
// Rejected (#1451) and does not wait for a maxConcurrentPromotions slot.
const supersededHold = `${[bundle.metadata.name].filter(x_, bundle.status.phase != "Superseded" && bundle.status.phase != "Rejected" && ` +
	`!(has(bundle.status.conditions) && bundle.status.conditions.exists(c_, c_.type == "WaitingForSlot" && c_.status == "True")))[0]}`

// TestBuilder_StepsHeldOnceBundleSuperseded verifies that every PromotionStep
// node, including roots, resolves spec.bundleName only while
// the Bundle is not Superseded (E2E-R20) or Rejected (#1451). Without it a Superseded Bundle's
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
// expression the way kro does. A Superseded or Rejected Bundle must produce "index out of
// bounds", which kro classifies as data-pending (pkg/graphengine/runtime/
// errors.go celDataPendingPatterns): the node stays Unresolved, is not applied
// and is not pruned. Every other phase, including the recoverable Failed,
// resolves to the Bundle name.
func TestBuilder_SupersededHoldIsDataPending(t *testing.T) {
	// Evaluate the expression the builder emits, not a copy of it, so the
	// test fails when the builder drops a hold.
	p := makeLinearPipeline("one", "test")
	result, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle("one-v1", p.Name)})
	require.NoError(t, err)
	var emitted string
	for _, n := range result.Graph.Spec.Nodes {
		if n.Template["kind"] == "PromotionStep" {
			emitted, _ = n.Template["spec"].(map[string]interface{})["bundleName"].(string)
		}
	}
	require.NotEmpty(t, emitted, "a PromotionStep node with spec.bundleName")
	env, err := cel.NewEnv(cel.Variable("bundle", cel.DynType))
	require.NoError(t, err)
	expr := strings.TrimSuffix(strings.TrimPrefix(emitted, "${"), "}")
	ast, iss := env.Compile(expr)
	require.NoError(t, iss.Err())
	prg, err := env.Program(ast)
	require.NoError(t, err)

	slot := func(status string) []interface{} {
		return []interface{}{
			map[string]interface{}{"type": "Ready", "status": "False"},
			map[string]interface{}{"type": "WaitingForSlot", "status": status},
		}
	}
	cases := []struct {
		name    string
		phase   string
		conds   []interface{}
		pending bool
	}{
		{phase: "Available"},
		{phase: "Promoting"},
		{phase: "Verified"},
		{phase: "Failed"},
		{phase: "Superseded", pending: true},
		// #1349: a Failed Bundle waiting for a maxConcurrentPromotions slot.
		{name: "Failed waiting for a slot", phase: "Failed", conds: slot("True"), pending: true},
		{name: "Failed with a free slot", phase: "Failed", conds: slot("False")},
		{name: "Promoting with conditions", phase: "Promoting", conds: slot("False")[:1]},
		{phase: "Rejected", pending: true},
	}
	for _, tc := range cases {
		name := tc.name
		if name == "" {
			name = tc.phase
		}
		t.Run(name, func(t *testing.T) {
			status := map[string]interface{}{"phase": tc.phase}
			if tc.conds != nil {
				status["conditions"] = tc.conds
			}
			out, _, err := prg.Eval(map[string]interface{}{"bundle": map[string]interface{}{
				"metadata": map[string]interface{}{"name": "app-v1"},
				"status":   status,
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
