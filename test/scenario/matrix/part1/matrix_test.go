// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package part1 runs part 1 of the scenario matrix (scenario.RunMatrix).
package part1

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestScenarioMatrix runs part 1 of the scenario.MatrixParts parts of the
// scenario matrix (scenario.MatrixRows). Covers SCEN-MATRIX-01,
// SCEN-REJECT-01, SCEN-ACTOR-01, SCEN-VERIFY-01.
func TestScenarioMatrix(t *testing.T) { scenario.RunMatrix(t, 1) }
