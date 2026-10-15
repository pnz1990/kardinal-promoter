// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package differential runs the differential phase of the enterprise agent load.
package differential

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/enterprise/agentcases"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestEnterprise_AgentDifferential is the enterprise with agents (agentcases.Run): the registry and admissions by every kind of principal checked against pkg/actor, with the cost's growth bounded.
// Covers SCEN-AGENT-DIFF-01.
func TestEnterprise_AgentDifferential(t *testing.T) { agentcases.Run(t, agentcases.Differential) }
