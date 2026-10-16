// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package part1 runs the seeded random interleavings of part 1 of the
// enterprise's day in the life (daycases.Seeded).
package part1

import (
	"fmt"
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/enterprise/daycases"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestEnterprise_DaySeeds1 is part 1 of the day with the seeded interleavings of daycases.SeedList, and the whole day too with daycases.Whole.
// Covers SCEN-DAY-02.
func TestEnterprise_DaySeeds1(t *testing.T) {
	for _, seed := range daycases.SeedList() {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			daycases.Seeded(t, 1, seed)
		})
		if daycases.Whole() {
			t.Run(fmt.Sprintf("whole/seed=%d", seed), func(t *testing.T) {
				t.Parallel()
				daycases.SeededWhole(t, seed)
			})
		}
	}
}
