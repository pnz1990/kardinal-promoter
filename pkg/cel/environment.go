// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cel

import (
	"fmt"

	"github.com/google/cel-go/cel"
)

// NewCELEnvironment creates a shared CEL environment for PolicyGate expression evaluation.
// Phase 1 registers bundle, schedule, and environment as dynamic map types.
// Referencing unregistered or nil attributes causes CEL evaluation error (fail-closed).
func NewCELEnvironment() (*cel.Env, error) {
	env, err := cel.NewEnv(
		// Phase 1: all context objects are maps for flexibility
		// The expression accesses nested attributes via dynamic dispatch.
		cel.Variable("bundle", cel.DynType),
		cel.Variable("schedule", cel.DynType),
		cel.Variable("environment", cel.DynType),
		// Phase 2+ (not populated in Phase 1, but declared to avoid compile errors)
		cel.Variable("metrics", cel.DynType),
		cel.Variable("previousBundle", cel.DynType),
	)
	if err != nil {
		return nil, fmt.Errorf("cel.NewEnv: %w", err)
	}
	return env, nil
}
