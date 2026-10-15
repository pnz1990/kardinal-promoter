// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package part8 runs part 8 of the scenario matrix (scenario.RunMatrix).
package part8

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestScenarioMatrix runs part 8 of the scenario.MatrixParts parts of the
// scenario matrix (scenario.MatrixRows).
func TestScenarioMatrix(t *testing.T) { scenario.RunMatrix(t, 8) }
