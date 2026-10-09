// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import "time"

// SetRebaseBackoff replaces git-push's rebase backoff for a test.
func SetRebaseBackoff(f func(int) time.Duration) (restore func()) {
	old := rebaseBackoff
	rebaseBackoff = f
	return func() { rebaseBackoff = old }
}

// MaxRebaseAttempts is maxRebaseAttempts.
const MaxRebaseAttempts = maxRebaseAttempts
