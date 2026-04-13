// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package cel provides a shared CEL environment for kardinal-promoter that
// mirrors the kro/krocodile Graph controller's CEL environment. This ensures
// PolicyGate expressions use the same extended function set as kro's
// readyWhen/propagateWhen expressions — json.*, maps.*, lists.*, random.*,
// and standard string/math extensions.
//
// IMPORTANT: This package is a transitional workaround pending krocodile's
// recheckAfter primitive. See docs/design/10-graph-first-architecture.md.
// Do not add new functionality here without explicit human approval.
// New callers outside pkg/reconciler/policygate are banned (AGENTS.md).
package cel

import (
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/cel/library"
)

// NewCELEnvironment creates a shared CEL environment for PolicyGate expression
// evaluation using the same extended libraries as kro's Graph controller.
//
// Functions available in expressions:
//
// Standard CEL extensions (via cel-go/ext):
//   - strings: format, lowerAscii, upperAscii, quote, indexOf, etc.
//
// kro CEL library extensions (same as kro readyWhen/propagateWhen):
//   - json.marshal(v)                   → JSON string
//   - json.unmarshal(s)                 → dynamic value
//   - map1.merge(map2)                  → merged map (m2 wins) [member function]
//   - lists.setAtIndex(l, i, v)         → new list with v at index i
//   - lists.insertAtIndex(l, i, v)      → new list with v inserted
//   - lists.removeAtIndex(l, i)         → new list with index i removed
//   - random.seededInt(min, max, seed)  → deterministic int from seed
//   - omit sentinel for conditional field inclusion
//
// Context variables (populated by PolicyGate reconciler buildContext()):
//   - bundle         — current Bundle spec and status (dynamic map)
//   - schedule       — time context: dayOfWeek, hour, isWeekend (dynamic map)
//   - environment    — target environment name and labels (dynamic map)
//   - metrics        — MetricCheck status values (dynamic map)
//   - upstream       — upstream environment soak times (dynamic map)
//   - previousBundle — last Verified Bundle for rollback detection (dynamic map)
func NewCELEnvironment() (*cel.Env, error) {
	env, err := cel.NewEnv(
		// Context variables — populated by PolicyGate reconciler buildContext()
		cel.Variable("bundle", cel.DynType),
		cel.Variable("schedule", cel.DynType),
		cel.Variable("environment", cel.DynType),
		cel.Variable("metrics", cel.DynType),
		cel.Variable("upstream", cel.DynType),
		cel.Variable("previousBundle", cel.DynType),

		// Standard CEL string/math extensions
		ext.Strings(),

		// kro CEL library extensions — same set used by kro Graph controller
		// for readyWhen/propagateWhen evaluation. This ensures PolicyGate
		// expressions can use the same functions as native kro CEL expressions.
		// Inlined from github.com/kubernetes-sigs/kro/pkg/cel/library (Apache 2.0).
		library.JSON(),
		library.Maps(),
		library.Lists(),
		library.Random(),
		library.Omit(),
	)
	if err != nil {
		return nil, fmt.Errorf("cel.NewEnv: %w", err)
	}
	return env, nil
}
