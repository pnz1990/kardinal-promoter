// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import "time"

// BundleWakesSteps exposes the Bundle watch predicate.
var BundleWakesSteps = bundleWakesSteps

// SetHistoryTimeout sets historyTimeout and returns a function that restores it.
func SetHistoryTimeout(d time.Duration) (restore func()) {
	old := historyTimeout
	historyTimeout = d
	return func() { historyTimeout = old }
}
