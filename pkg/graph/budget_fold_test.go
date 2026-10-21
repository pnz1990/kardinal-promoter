// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestFleet_BudgetFold: an agent's budget rules fold into its fleet
// targets' pacing (docs/design/24-agent-budgets.md section 8): the wave
// creates no more in-flight targets than the strictest of the fleet's
// maxConcurrent, the rules' maxConcurrent and their fleetShare (rounded up,
// at least one), and stops at the strictest maxUnavailable. A rule that
// does not apply to the fleet changes nothing, so the Graph is byte for byte
// the one a person's version gets.
// Covers BUDGET-FOLD-01.
func TestFleet_BudgetFold(t *testing.T) {
	rule := func(envs ...string) kardinalv1alpha1.AgentBudgetRule {
		return kardinalv1alpha1.AgentBudgetRule{Name: "prod", Environments: envs}
	}
	inFlight := func(t *testing.T, fleetMax int, rules ...kardinalv1alpha1.AgentBudgetRule) int {
		t.Helper()
		res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: fleetPipeline(fleetMax),
			Bundle: makeBundle("app-x7k2m", "app"), AgentBudgets: rules})
		require.NoError(t, err)
		assertKroValid(t, res.Graph)
		sim := newCompactSim(t, res.Graph)
		sim.fleet = map[string]string{"prod-eu": "prod", "prod-us": "prod", "prod-ap": "prod"}
		sim.steps["test"] = "Verified"
		n := 0
		for _, e := range sim.advance() {
			if sim.fleet[e] != "" {
				n++
			}
		}
		return n
	}
	concurrent := func(n int32, envs ...string) kardinalv1alpha1.AgentBudgetRule {
		r := rule(envs...)
		r.MaxConcurrent = n
		return r
	}
	share := func(s string, envs ...string) kardinalv1alpha1.AgentBudgetRule {
		r := rule(envs...)
		r.FleetShare = s
		return r
	}
	for _, tc := range []struct {
		name     string
		fleetMax int
		rules    []kardinalv1alpha1.AgentBudgetRule
		want     int
	}{
		{"no rules: the fleet's two", 2, nil, 2},
		{"a rule of 1 on the fleet", 2, []kardinalv1alpha1.AgentBudgetRule{concurrent(1, "prod")}, 1},
		{"a rule of 5 is looser than the fleet's two", 2, []kardinalv1alpha1.AgentBudgetRule{concurrent(5, "prod")}, 2},
		{"a rule on an unlimited fleet", 0, []kardinalv1alpha1.AgentBudgetRule{concurrent(2, "prod*")}, 2},
		{"fleetShare 34% of 3 is 2 (rounded up)", 0, []kardinalv1alpha1.AgentBudgetRule{share("34%", "prod")}, 2},
		{"fleetShare 1% is at least one", 0, []kardinalv1alpha1.AgentBudgetRule{share("1%", "prod")}, 1},
		{"several rules: the strictest", 0, []kardinalv1alpha1.AgentBudgetRule{share("100%", "prod"), concurrent(1, "prod-*")}, 1},
		{"a rule on another environment", 2, []kardinalv1alpha1.AgentBudgetRule{concurrent(1, "uat")}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, inFlight(t, tc.fleetMax, tc.rules...))
		})
	}

	t.Run("maxUnavailable: one failure stops the fleet", func(t *testing.T) {
		r := rule("prod")
		r.MaxUnavailable = 1
		res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: fleetPipeline(2),
			Bundle: makeBundle("app-x7k2m", "app"), AgentBudgets: []kardinalv1alpha1.AgentBudgetRule{r}})
		require.NoError(t, err)
		sim := newCompactSim(t, res.Graph)
		sim.fleet = map[string]string{"prod-eu": "prod", "prod-us": "prod", "prod-ap": "prod"}
		sim.steps["test"] = "Verified"
		assert.Equal(t, []string{"prod-eu", "prod-us", "test"}, sim.advance())
		sim.steps["prod-eu"], sim.steps["prod-us"] = "Failed", "Verified"
		assert.NotContains(t, sim.advance(), "prod-ap", "no target starts after the agent's one failure")
	})

	t.Run("a rule that does not apply leaves the Graph byte for byte", func(t *testing.T) {
		build := func(rules []kardinalv1alpha1.AgentBudgetRule) []byte {
			res, err := graph.NewBuilder().Build(graph.BuildInput{Pipeline: fleetPipeline(2),
				Bundle: makeBundle("app-x7k2m", "app"), AgentBudgets: rules})
			require.NoError(t, err)
			b, err := json.Marshal(res.Graph)
			require.NoError(t, err)
			return b
		}
		other := rule("uat")
		other.MaxConcurrent = 1
		assert.Equal(t, string(build(nil)), string(build([]kardinalv1alpha1.AgentBudgetRule{other})))
	})
}
