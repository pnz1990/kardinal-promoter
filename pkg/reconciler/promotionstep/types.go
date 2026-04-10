// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Reconciler drives the PromotionStep state machine.
// Each PromotionStep object transitions through: Pending → Running → Succeeded | Failed | Blocked.
// Implemented in Stage 6 (spec 003).
type Reconciler interface {
	// Reconcile processes a single PromotionStep.
	Reconcile(ctx context.Context, step *kardinalv1alpha1.PromotionStep) error
}

// Phase constants for PromotionStep.status.phase.
const (
	PhasePending   = "Pending"
	PhaseRunning   = "Running"
	PhaseSucceeded = "Succeeded"
	PhaseFailed    = "Failed"
	PhaseBlocked   = "Blocked"
)
