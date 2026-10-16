// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package chaos restarts the controller mid-wave (agentcases.RunChaos).
package chaos

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/enterprise/agentcases"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestEnterprise_ControllerRestarts is the controller killed at random side
// effects of waves of agent-shipped versions, and started again, per seed
// (agentcases.RunChaos): no PR twice, no AuditEvent lost or twice, no
// promotion twice.
// Covers SCEN-CHAOS-RESTART-01.
func TestEnterprise_ControllerRestarts(t *testing.T) { agentcases.RunChaos(t) }
