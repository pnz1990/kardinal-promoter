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
