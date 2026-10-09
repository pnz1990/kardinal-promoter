// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestContendedDelay (#1504 QA): the jittered backoff of a contended step is
// between half and one and a half of the delay, spreads (writers that
// collided do not come back together), and never exceeds retryMaxDelay.
func TestContendedDelay(t *testing.T) {
	for _, d := range []time.Duration{retryBaseDelay, 40 * time.Second, retryMaxDelay} {
		seen := map[time.Duration]bool{}
		for range 500 {
			got := contendedDelay(d)
			assert.GreaterOrEqual(t, got, d/2, d)
			assert.Less(t, got, d/2+d, d)
			assert.LessOrEqual(t, got, retryMaxDelay, d)
			seen[got] = true
		}
		assert.Greater(t, len(seen), 100, "%s: jittered, not fixed", d)
	}
	assert.Equal(t, time.Duration(0), contendedDelay(0))
}
