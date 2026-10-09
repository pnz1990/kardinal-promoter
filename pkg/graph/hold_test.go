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

// TestBuilder_HeldEnvironment (#1528): while the Pipeline holds prod on a
// rollback Bundle, the prod PromotionStep node of every other Bundle's Graph
// never resolves spec.bundleName (data-pending: the node is not applied), and
// the hold's own Bundle resolves it as usual. Other environments are not
// affected, and the Graph stays valid for kro.
//
// Covers RB-HOLD-04.
func TestBuilder_HeldEnvironment(t *testing.T) {
	p := makeLinearPipeline("app", "test", "uat", "prod")
	p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "app-rollback-abc123", Reason: "INC-42"}}

	emitted := func(bundle string) map[string]string {
		result, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: p, Bundle: makeBundle(bundle, "app")})
		require.NoError(t, err)
		assertKroValid(t, result.Graph)
		out := map[string]string{}
		for _, n := range result.Graph.Spec.Nodes {
			if n.Template["kind"] != "PromotionStep" {
				continue
			}
			spec := n.Template["spec"].(map[string]interface{})
			out[spec["environment"].(string)] = spec["bundleName"].(string)
		}
		return out
	}
	eval := func(t *testing.T, expr, name string) (string, error) {
		t.Helper()
		env, err := cel.NewEnv(cel.Variable("bundle", cel.DynType))
		require.NoError(t, err)
		ast, iss := env.Compile(strings.TrimSuffix(strings.TrimPrefix(expr, "${"), "}"))
		require.NoError(t, iss.Err())
		prg, err := env.Program(ast)
		require.NoError(t, err)
		out, _, err := prg.Eval(map[string]interface{}{"bundle": map[string]interface{}{
			"metadata": map[string]interface{}{"name": name},
			"status":   map[string]interface{}{"phase": "Promoting"},
		}})
		if err != nil {
			return "", err
		}
		return out.Value().(string), nil
	}

	other := emitted("app-v3")
	assert.Equal(t, supersededHold, other["test"], "test is not held")
	assert.Equal(t, supersededHold, other["uat"], "uat is not held")
	assert.Contains(t, other["prod"], `bundle.metadata.name == "app-rollback-abc123"`)
	_, err := eval(t, other["prod"], "app-v3")
	require.Error(t, err, "another Bundle gets no step in the held environment")
	assert.Contains(t, err.Error(), "index out of bounds", "data-pending, so kro neither applies nor prunes the node")

	own := emitted("app-rollback-abc123")
	got, err := eval(t, own["prod"], "app-rollback-abc123")
	require.NoError(t, err)
	assert.Equal(t, "app-rollback-abc123", got, "the hold's Bundle promotes into the held environment")

	// Orphaned (#1629: the Pipeline reconciler found its Bundle missing past
	// the grace): the hold holds nothing.
	p.Status.HoldStates = []kardinalv1alpha1.EnvironmentHoldState{
		{Environment: "prod", Bundle: "app-rollback-abc123", State: kardinalv1alpha1.HoldStateOrphaned}}
	assert.Equal(t, supersededHold, emitted("app-v3")["prod"], "orphaned: the condition is gone")
	p.Status.HoldStates[0].State = kardinalv1alpha1.HoldStateBundleMissing
	assert.Contains(t, emitted("app-v3")["prod"], `bundle.metadata.name == "app-rollback-abc123"`, "within the grace it holds")
	p.Status.HoldStates = nil

	p.Spec.Holds = nil
	assert.Equal(t, supersededHold, emitted("app-v3")["prod"], "released: the condition is gone")
}
