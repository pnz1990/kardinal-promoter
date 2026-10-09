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

// HoldBranchTurn takes the turn of the branch of repoURL for step (a
// namespace/name key) on r, as a reconcile pushing to it does, and returns
// the release.
func HoldBranchTurn(r *Reconciler, repoURL, branch, step string) (release func()) {
	key, _ := basePushBranch([]string{"git-clone", "git-push"}, 0, repoURL, branch)
	rel, _, ok := r.pushTurns.tryTurn(key, step, time.Now())
	if !ok {
		panic("branch turn already held")
	}
	return func() { rel(time.Now()) }
}
