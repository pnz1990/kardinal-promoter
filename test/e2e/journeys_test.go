// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package e2e

import (
	"testing"
)

// TestJourney1Quickstart validates docs/aide/definition-of-done.md Journey 1.
//
// A user installs kardinal-promoter, applies examples/quickstart/pipeline.yaml,
// creates a Bundle, and the system promotes through test → uat → prod with
// a PR opened for prod and PolicyGates evaluated correctly.
//
// This test is a stub. Implementation is part of Stage 6 (PromotionStep Reconciler)
// and Stage 10 (PR Evidence). See docs/aide/definition-of-done.md Journey 1.
func TestJourney1Quickstart(t *testing.T) {
	t.Skip("Journey 1: not yet implemented — requires Stages 0-8")
}

// TestJourney2MultiClusterFleet validates docs/aide/definition-of-done.md Journey 2.
//
// A user applies examples/multi-cluster-fleet/pipeline.yaml and the system
// promotes through test → pre-prod → [prod-eu, prod-us] in parallel with
// Argo Rollouts canary delegation and Argo CD hub-spoke health verification.
//
// This test is a stub. Implementation is part of Stage 7 (Health Adapters) and
// Stage 14 (Distributed Mode). See docs/aide/definition-of-done.md Journey 2.
func TestJourney2MultiClusterFleet(t *testing.T) {
	t.Skip("Journey 2: not yet implemented — requires Stages 0-8, 11, 14")
}

// TestJourney3PolicyGovernance validates docs/aide/definition-of-done.md Journey 3.
//
// A user creates a no-weekend-deploys PolicyGate and verifies it blocks prod
// promotion. `kardinal policy simulate --time "Saturday 3pm"` returns BLOCKED.
// `kardinal explain` shows gate evaluation with current values.
//
// This test is a stub. Implementation is part of Stage 4 (PolicyGate CEL Evaluator)
// and Stage 8 (CLI). See docs/aide/definition-of-done.md Journey 3.
func TestJourney3PolicyGovernance(t *testing.T) {
	t.Skip("Journey 3: not yet implemented — requires Stages 0-5, 8")
}

// TestJourney4Rollback validates docs/aide/definition-of-done.md Journey 4.
//
// After a bad Bundle reaches prod, `kardinal rollback` opens a PR with
// the kardinal/rollback label and the same evidence structure as a forward
// promotion. After merge, the environment reflects the rolled-back version.
//
// This test is a stub. Implementation is part of Stage 13 (Rollback).
// See docs/aide/definition-of-done.md Journey 4.
func TestJourney4Rollback(t *testing.T) {
	t.Skip("Journey 4: not yet implemented — requires Stages 0-7, 13")
}

// TestJourney5CLI validates docs/aide/definition-of-done.md Journey 5.
//
// Every CLI command in docs/cli-reference.md executes without error and
// produces output matching the documented format.
//
// This test is a stub. Implementation is part of Stage 8 (CLI).
// See docs/aide/definition-of-done.md Journey 5.
func TestJourney5CLI(t *testing.T) {
	t.Skip("Journey 5: not yet implemented — requires Stages 0-9")
}
