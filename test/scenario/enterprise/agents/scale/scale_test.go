// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package scale runs the enterprise at the scale of agents
// (agentcases.RunScale).
package scale

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/enterprise/agentcases"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestEnterprise_AgentScale is 7,500 AgentIdentities, 200 Pipelines in 50
// tenants and bursts of agent-shipped versions in full (the release gate;
// 2,000 in CI's scenario-enterprise job): every burst converges
// within its budget of rounds and reconciles, and admission p99 stays within
// hack/ci/perf-baseline.txt.
// Covers SCEN-AGENT-SCALE-01.
func TestEnterprise_AgentScale(t *testing.T) { agentcases.RunScale(t) }
