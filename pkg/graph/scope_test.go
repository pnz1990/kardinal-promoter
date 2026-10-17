// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// #1720: a gate's scope is the one it behaves with. A gate in an org policy
// namespace is org, labelled or not; a gate labelled org is org anywhere;
// any other gate is its label, team by default.
func TestGateScope(t *testing.T) {
	gate := func(ns, scope string) kardinalv1alpha1.PolicyGate {
		g := makePolicyGate("g", ns, "prod", "true")
		if scope != "" {
			g.Labels["kardinal.io/scope"] = scope
		}
		return g
	}
	for _, tc := range []struct {
		name     string
		gate     kardinalv1alpha1.PolicyGate
		policyNS []string
		want     string
	}{
		{"policy namespace, no label", gate("platform-policies", ""), nil, "org"},
		{"policy namespace, labelled team", gate("platform-policies", "team"), nil, "org"},
		{"team namespace, labelled org", gate("team-a", "org"), nil, "org"},
		{"team namespace, no label", gate("team-a", ""), nil, "team"},
		{"team namespace, other label", gate("team-a", "system"), nil, "system"},
		{"configured policy namespace", gate("corp", ""), []string{"corp"}, "org"},
		{"default namespace not configured", gate("platform-policies", ""), []string{"corp"}, "team"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, graph.GateScope(tc.gate, tc.policyNS))
		})
	}
}

// #1720: the builder stamps an instance with that scope, so an unlabelled
// gate in platform-policies is shown as org on its instances.
func TestBuild_InstanceScopeFromNamespace(t *testing.T) {
	res, err := graph.NewBuilder().Build(graph.BuildInput{
		Pipeline: makeLinearPipeline("app", "test", "prod"),
		Bundle:   makeBundle("app-v1", "app"),
		PolicyGates: []kardinalv1alpha1.PolicyGate{
			makePolicyGate("platform", "platform-policies", "prod", "true"),
			makePolicyGate("local", "default", "prod", "true"),
		},
	})
	require.NoError(t, err)
	want := map[string]string{"platform": "org", "local": "team"}
	rendered := gateInstances(t, res.Graph)["prod"]
	require.Len(t, rendered, 2)
	for _, inst := range rendered {
		labels := objLabels(inst)
		template := labels["kardinal.io/gate-template"].(string)
		assert.Equal(t, want[template], labels["kardinal.io/scope"], "rendered instance of %s", template)
	}
	require.Len(t, res.GateInstances, 2)
	for _, inst := range res.GateInstances {
		assert.Equal(t, want[inst.Labels["kardinal.io/gate-template"]], inst.Labels["kardinal.io/scope"], "mirror of %s", inst.Name)
	}
}
