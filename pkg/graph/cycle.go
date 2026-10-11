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
// It runs the same resolveOrdering that Build uses. The Pipeline reconciler
// calls it to set Ready=False/ValidationFailed, and the Bundle reconciler
// calls it to name the reason a Bundle failed.
func DetectCycle(pipeline *kardinalv1alpha1.Pipeline) error {
	// The ordering as written: fleet targets add no edge a cycle could need,
	// and a selector fleet may not be resolved yet.
	_, _, err := resolveSpecOrdering(pipeline)
	return err
}

// EnvironmentDependencies returns each environment's upstream environments
// as the builder wires them: explicit dependsOn and wave edges, otherwise the
// previous environment in the list. The CLI uses it to tell a Bundle that is
// waiting at an environment's gates from one that has not reached it.
func EnvironmentDependencies(pipeline *kardinalv1alpha1.Pipeline) (map[string][]string, error) {
	_, deps, err := resolveOrdering(pipeline)
	return deps, err
}
