// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package part1 runs part 1 of the verify scenarios (verifycases.Run).
package part1

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/verifycases"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestScenarioVerify runs part 1 of the verify scenarios: verify gates end to
// end, the Graph, the AgentRun reconciler, agents' Verdicts through
// admission and the gate's tally. Covers SCEN-VERIFY-02.
func TestScenarioVerify(t *testing.T) { verifycases.Run(t, 1, verifycases.Parts) }
