// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package part4 runs part 4 of the enterprise's day in the life in its
// fixed order (daycases.Timeline).
package part4

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/enterprise/daycases"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestEnterprise_DayTimeline4 is part 4 of the day's episodes (daycases.Episodes), one after another.
// Covers SCEN-DAY-01.
func TestEnterprise_DayTimeline4(t *testing.T) { daycases.Timeline(t, 4) }
