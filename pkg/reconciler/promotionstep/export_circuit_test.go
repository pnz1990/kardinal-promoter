// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import "time"

// NoCircuitJitter turns off the jitter of circuit waits until restore.
func NoCircuitJitter() (restore func()) {
	old := circuitJitter
	circuitJitter = func(d time.Duration) time.Duration { return d }
	return func() { circuitJitter = old }
}
