// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package load runs the load phase of the enterprise agent load.
package load

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/enterprise/agentcases"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestEnterprise_AgentLoad is the enterprise with agents (agentcases.Run): the registry, bursts of versions by agents across the teams, suspension, stamps, isolation, bounded reconciles and the index overflow.
// Covers SCEN-AGENT-LOAD-01.
func TestEnterprise_AgentLoad(t *testing.T) { agentcases.Run(t, agentcases.Load) }

// TestEnterprise_AgentBudgets is the enterprise with agents and agent budgets on (agentcases.RunBudgets): a bursting agent's prod versions beyond maxConcurrent 1 and versionsPerDay 3 are held, never refused or failed, while the others converge, and its ledger stays within the rule at every round; held versions go on when a grant settles and when the window rolls over, and a superseded held one leaves the queue ungranted.
// Covers SCEN-AGENT-BUDGET-01, SCEN-AGENT-BUDGET-02.
func TestEnterprise_AgentBudgets(t *testing.T) { agentcases.RunBudgets(t) }
