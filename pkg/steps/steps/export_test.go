// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import "time"

// SetAutoMergeRetryDelays replaces autoMergeRetryDelays for a test and
// returns a function that restores them.
func SetAutoMergeRetryDelays(d []time.Duration) (restore func()) {
	old := autoMergeRetryDelays
	autoMergeRetryDelays = d
	return func() { autoMergeRetryDelays = old }
}
