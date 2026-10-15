// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package part2 runs part 2 of the verify scenarios (verifycases.Run).
package part2

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/verifycases"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestScenarioVerify runs part 2 of the verify scenarios: verify gates end to
// end, the Graph, the AgentRun reconciler, agents' Verdicts through
// admission and the gate's tally.
func TestScenarioVerify(t *testing.T) { verifycases.Run(t, 2, verifycases.Parts) }
