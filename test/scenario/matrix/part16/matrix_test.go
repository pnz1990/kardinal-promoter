// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package part16 runs part 16 of the scenario matrix (scenario.RunMatrix).
package part16

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestScenarioMatrix runs part 16 of the scenario.MatrixParts parts of the
// scenario matrix (scenario.MatrixRows).
func TestScenarioMatrix(t *testing.T) { scenario.RunMatrix(t, 16) }
