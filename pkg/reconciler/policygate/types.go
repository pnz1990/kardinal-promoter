// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// PolicyGateEvaluator is the interface for evaluating a PolicyGate instance.
// This stub is kept for documentation purposes; the concrete Reconciler implements
// controller-runtime's reconcile.Reconciler interface directly.
type PolicyGateEvaluator interface {
	// Evaluate evaluates the PolicyGate CEL expression and updates status.
	Evaluate(ctx context.Context, gate *kardinalv1alpha1.PolicyGate) error
}
