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
// Requires: Stages 0–8 complete (Graph Builder, PolicyGate CEL, PromotionStep
// Reconciler, SCM/PR, Health Adapters, CLI).
func TestJourney1Quickstart(t *testing.T) {
	infraClient(t) // skip if no cluster
	t.Skip("Journey 1: not yet implemented — requires Stages 4-8 (current: Stage 4 in progress)")
}

// TestJourney2MultiClusterFleet validates docs/aide/definition-of-done.md Journey 2.
//
// A user applies examples/multi-cluster-fleet/pipeline.yaml and the system
// promotes through test → pre-prod → [prod-eu, prod-us] in parallel with
// Argo Rollouts canary delegation and Argo CD hub-spoke health verification.
//
// Requires: Stages 0–8, 11, 14 complete.
func TestJourney2MultiClusterFleet(t *testing.T) {
	infraClient(t)
	t.Skip("Journey 2: not yet implemented — requires Stages 4-8, 11, 14")
}

// TestJourney3PolicyGovernance validates docs/aide/definition-of-done.md Journey 3.
//
// A user creates a no-weekend-deploys PolicyGate and verifies it blocks prod
// promotion. `kardinal policy simulate --time "Saturday 3pm"` returns BLOCKED.
// `kardinal explain` shows gate evaluation with current values.
//
// Requires: Stages 0–5, 8 complete.
func TestJourney3PolicyGovernance(t *testing.T) {
	infraClient(t)
	t.Skip("Journey 3: not yet implemented — requires Stages 4-5, 8")
}

// TestJourney4Rollback validates docs/aide/definition-of-done.md Journey 4.
//
// After a bad Bundle reaches prod, `kardinal rollback` opens a PR with
// the kardinal/rollback label and the same evidence structure as a forward
// promotion. After merge, the environment reflects the rolled-back version.
//
// Requires: Stages 0–7, 13 complete.
func TestJourney4Rollback(t *testing.T) {
	infraClient(t)
	t.Skip("Journey 4: not yet implemented — requires Stages 4-7, 13")
}

// TestJourney5CLI validates docs/aide/definition-of-done.md Journey 5.
//
// Every CLI command in docs/cli-reference.md executes without error and
// produces output matching the documented format.
//
// Requires: Stages 0–9 complete.
func TestJourney5CLI(t *testing.T) {
	infraClient(t)
	t.Skip("Journey 5: not yet implemented — requires Stages 4-9")
}
