// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package part5 runs the seeded random interleavings of part 5 of the
// enterprise's day in the life (daycases.Seeded).
package part5

import (
	"fmt"
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/enterprise/daycases"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestEnterprise_DaySeeds5 is part 5 of the day with the seeded interleavings of daycases.SeedList.
// Covers SCEN-DAY-02.
func TestEnterprise_DaySeeds5(t *testing.T) {
	for _, seed := range daycases.SeedList(5) {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			daycases.Seeded(t, 5, seed)
		})
	}
}
