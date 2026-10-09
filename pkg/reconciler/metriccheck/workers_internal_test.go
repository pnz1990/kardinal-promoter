// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWorkerCount: the MetricCheck controller has at least 16 workers and
// always spareWorkers more than the global query slots, so every slot can be
// used and checks without a query are served while the slots are busy
// (#1551 finding: --metriccheck-global-slots above 16 ran at most 16
// queries).
func TestWorkerCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limiter *Limiter
		want    int
	}{
		{"no limiter", nil, 16},
		{"default slots (12)", NewLimiter(DefaultGlobalSlots, DefaultNamespaceSlots), 16},
		{"fewer slots", NewLimiter(2, 1), 16},
		{"slots at the worker count", NewLimiter(16, 1), 20},
		{"many slots", NewLimiter(40, 4), 44},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := workerCount(tc.limiter)
			assert.Equal(t, tc.want, got)
			if tc.limiter != nil {
				assert.GreaterOrEqual(t, got-tc.limiter.Global, spareWorkers, "workers to spare beyond the slots")
			}
		})
	}
}
