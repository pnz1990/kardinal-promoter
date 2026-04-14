// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// cel_evaluator.go — CEL evaluation for PolicyGate expressions.
//
// This file contains the logic previously in pkg/cel/ (the transitional workaround
// documented in docs/design/10-graph-first-architecture.md). Moving it here
// eliminates the separate pkg/cel package (#130) while keeping the functionality
// entirely within pkg/reconciler/policygate — the one allowed location.
//
// The kro library extensions (pkg/cel/library) are still used here; that import
// is explicitly allowed (see AGENTS.md §Anti-Patterns).
package policygate

import (
	"fmt"
	"sync"

	goccel "github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/ext"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/cel/library"
)

// evaluator wraps a goccel.Env and provides cached CEL expression evaluation.
// All evaluation errors are fail-closed: a failing or erroring expression
// returns (false, reason, err) — it does not permit the gate to pass.
//
// EvaluateExpression evaluates a CEL expression against the given context map.
// Used by the UI validate-cel endpoint and e2e tests.
// Returns (pass, reason, error). All errors are fail-closed (pass=false).
func (r *Reconciler) EvaluateExpression(expr string, ctx map[string]interface{}) (bool, string, error) {
	return r.eval.evaluate(expr, ctx)
}

// The program cache is keyed by expression string. Cache is valid for the
// lifetime of the Evaluator (reset on controller restart).
type evaluator struct {
	env   *goccel.Env
	cache map[string]goccel.Program
	mu    sync.Mutex
}

// newEvaluator creates an Evaluator backed by a CEL environment that includes
// all kro library extensions — the same function set as kro Graph readyWhen
// expressions. This ensures PolicyGate expressions are compatible with Graph CEL.
//
// Functions available in expressions:
//   - Standard strings (cel-go/ext)
//   - json.marshal(v) / json.unmarshal(s)
//   - maps.merge(map1, map2)
//   - lists.setAtIndex / insertAtIndex / removeAtIndex
//   - random.seededInt(min, max, seed)
//   - changewindow.isAllowed(name) → bool  (true when the named window is NOT active/blocking)
//   - changewindow.isBlocked(name) → bool  (true when the named window IS active/blocking)
//
// Context variables (populated by buildContext):
//   - bundle, schedule, environment, metrics, upstream, previousBundle, changewindow
func newEvaluator() (*evaluator, error) {
	env, err := goccel.NewEnv(
		goccel.Variable("bundle", goccel.DynType),
		goccel.Variable("schedule", goccel.DynType),
		goccel.Variable("environment", goccel.DynType),
		goccel.Variable("metrics", goccel.DynType),
		goccel.Variable("upstream", goccel.DynType),
		goccel.Variable("previousBundle", goccel.DynType),
		goccel.Variable("changewindow", goccel.DynType),
		ext.Strings(),
		library.JSON(),
		library.Maps(),
		library.Lists(),
		library.Random(),
		library.Omit(),
		// changewindow.isAllowed(name) → bool
		// Returns true when the named ChangeWindow is NOT currently blocking.
		// Equivalent to: !changewindow["name"]
		// Example: changewindow.isAllowed("business-hours") — passes during business hours
		goccel.Function("isAllowed",
			goccel.MemberOverload(
				"changewindow_isAllowed_string",
				[]*goccel.Type{goccel.DynType, goccel.StringType},
				goccel.BoolType,
				goccel.BinaryBinding(func(mapVal ref.Val, nameVal ref.Val) ref.Val {
					name, ok := nameVal.Value().(string)
					if !ok {
						return types.Bool(false)
					}
					cwMap, ok := mapVal.Value().(map[string]interface{})
					if !ok {
						// changewindow variable is not a map — fail-closed (deny).
						return types.Bool(false)
					}
					active, _ := cwMap[name].(bool)
					// isAllowed → window must NOT be active (blocking).
					return types.Bool(!active)
				}),
			),
		),
		// changewindow.isBlocked(name) → bool
		// Returns true when the named ChangeWindow IS currently blocking.
		// Equivalent to: changewindow["name"]
		// Example: !changewindow.isBlocked("holiday-freeze") — passes when freeze is not active
		goccel.Function("isBlocked",
			goccel.MemberOverload(
				"changewindow_isBlocked_string",
				[]*goccel.Type{goccel.DynType, goccel.StringType},
				goccel.BoolType,
				goccel.BinaryBinding(func(mapVal ref.Val, nameVal ref.Val) ref.Val {
					name, ok := nameVal.Value().(string)
					if !ok {
						return types.Bool(false)
					}
					cwMap, ok := mapVal.Value().(map[string]interface{})
					if !ok {
						// changewindow variable is not a map — fail-closed (active/blocked).
						return types.Bool(true)
					}
					active, _ := cwMap[name].(bool)
					// isBlocked → window is active (blocking).
					return types.Bool(active)
				}),
			),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("cel.NewEnv: %w", err)
	}
	return &evaluator{
		env:   env,
		cache: make(map[string]goccel.Program),
	}, nil
}

// evaluate compiles (or retrieves from cache) and evaluates the CEL expression
// against the provided context map.
//
// Returns:
//   - pass: true if the expression evaluates to true
//   - reason: human-readable explanation of the result
//   - err: non-nil if compilation or evaluation failed (implies pass=false)
//
// All errors are fail-closed: the gate does not pass on any error.
func (e *evaluator) evaluate(expr string, ctx map[string]interface{}) (bool, string, error) {
	prg, err := e.getOrCompile(expr)
	if err != nil {
		return false, fmt.Sprintf("CEL compile error: %s", err), err
	}

	out, _, err := prg.Eval(ctx)
	if err != nil {
		return false, fmt.Sprintf("CEL evaluation error: %s", err), err
	}

	result, ok := out.Value().(bool)
	if !ok {
		err := fmt.Errorf("CEL expression %q returned non-boolean: %T(%v)", expr, out.Value(), out.Value())
		return false, err.Error(), err
	}

	return result, fmt.Sprintf("%s = %v", expr, result), nil
}

// validate compiles the CEL expression and returns a non-nil error if it has
// a syntax or type error. It does NOT evaluate the expression.
func (e *evaluator) validate(expr string) error {
	_, err := e.getOrCompile(expr)
	return err
}

// EvaluateForTest evaluates a CEL expression in a given context.
// This is exported only for use by tests that test the evaluator directly
// (e.g. policy simulate tests, kro library function tests).
// Production code always goes through the Reconciler.
func EvaluateForTest(expr string, ctx map[string]interface{}) (bool, string, error) {
	ev, err := newEvaluator()
	if err != nil {
		return false, "", fmt.Errorf("newEvaluator: %w", err)
	}
	return ev.evaluate(expr, ctx)
}
func (e *evaluator) getOrCompile(expr string) (goccel.Program, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if prg, ok := e.cache[expr]; ok {
		return prg, nil
	}

	ast, issues := e.env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}

	prg, err := e.env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("cel.Program: %w", err)
	}

	e.cache[expr] = prg
	return prg, nil
}
