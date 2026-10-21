// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package part5 runs part 5 of the enterprise's day in the life in its
// fixed order (daycases.Timeline).
package part5

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/enterprise/daycases"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestEnterprise_DayTimeline5 is part 5 of the day's episodes (daycases.Episodes), one after another.
// Covers SCEN-DAY-01, SCEN-DAY-BUDGET-01.
func TestEnterprise_DayTimeline5(t *testing.T) { daycases.Timeline(t, 5) }
