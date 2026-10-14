// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package shard

import "time"

// Test-only helpers: production code does not call these (make hygiene).

// FenceMargin is the worst-case time between this shard cancelling its
// reconciles and another shard taking its namespaces (see fence).
func FenceMargin() time.Duration {
	takeover := time.Duration(float64(leaseDuration) * (1 - maxClockRate))
	stop := time.Duration(float64(leaseDuration-fenceMargin)*(1+maxClockRate)) + fenceTick
	return takeover - stop
}
