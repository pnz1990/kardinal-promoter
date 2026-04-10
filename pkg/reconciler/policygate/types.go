// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Reconciler evaluates PolicyGate CEL expressions and patches gate status.
// Implemented in Stage 4 (spec 004).
type Reconciler interface {
	// Reconcile evaluates the PolicyGate CEL expression and updates status.
	Reconcile(ctx context.Context, gate *kardinalv1alpha1.PolicyGate) error
}

// Result constants for PolicyGate.status.result.
const (
	ResultPass    = "Pass"
	ResultFail    = "Fail"
	ResultPending = "Pending"
)
