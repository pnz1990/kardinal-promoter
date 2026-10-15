// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package part2 runs part of the actor-aware policy scenarios (policycases).
package part2

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/policycases"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestScenarioAgentsShorthand runs policycases.AgentsShorthand. Covers SCEN-POLICY-AGENTS-01.
func TestScenarioAgentsShorthand(t *testing.T) { policycases.AgentsShorthand(t) }
