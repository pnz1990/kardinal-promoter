// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// DetectCycle checks whether a Pipeline's dependsOn relationships contain a
// circular dependency. It returns a non-nil error with a human-readable cycle
// path (e.g. "prod → uat → prod (cycle!)") when a cycle is found.
//
// This function is a pure, side-effect-free predicate — it performs no I/O.
// It is called from both the ValidatingAdmissionWebhook (fast-fail at apply
// time) and from resolveOrdering in the bundle translator (fail at Graph
// build time if the webhook was not in place).
//
// Design ref: docs/design/15-production-readiness.md §Lens 4
func DetectCycle(pipeline *kardinalv1alpha1.Pipeline) error {
	_, _, err := resolveOrdering(pipeline)
	return err
}
