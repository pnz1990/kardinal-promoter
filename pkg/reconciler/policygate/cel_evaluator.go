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
	"context"
	"fmt"
	"sync"
	"time"

	goccel "github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/ext"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/cel/library"
)

// Limits on one gate expression. Any gate author can write an expression, and
// all gates share the controller's PolicyGate workers, so an expression must
// not be able to compute or allocate without bound (C04-gates-25).
const (
	// celCostLimit is the runtime cost budget of one evaluation, the same
	// per-expression limit Kubernetes uses for CRD validation rules.
	celCostLimit = 1_000_000
	// celEvalTimeout bounds the wall-clock time of one evaluation.
	celEvalTimeout = time.Second
	// celInterruptCheckFrequency is how many comprehension iterations run
	// between checks of the evaluation deadline.
	celInterruptCheckFrequency = 100
	// maxCachedPrograms bounds the compiled-program cache. When it is full the
	// cache is cleared; programs are recompiled on demand.
	maxCachedPrograms = 1024
)

// evaluator wraps a goccel.Env and provides cached CEL expression evaluation.
// All evaluation errors are fail-closed: a failing or erroring expression
// returns (false, reason, err) — it does not permit the gate to pass.
//
// The program cache is keyed by expression string and bounded by
// maxCachedPrograms. It only saves compilation work: it holds no gate state.
type evaluator struct {
	env   *goccel.Env
	cache map[string]goccel.Program
	mu    sync.Mutex
	// timeout bounds one evaluation; celEvalTimeout unless a test changes it.
	timeout time.Duration
}

// ValidateExpression compiles a PolicyGate expression in the same CEL
// environment the reconciler evaluates it in, without evaluating it. The UI
// validate-cel endpoint uses it, so the UI accepts exactly the expressions the
// controller accepts (C04-gates-26).
func ValidateExpression(expr string) error {
	ev, err := newEvaluator()
	if err != nil {
		return fmt.Errorf("policygate CEL environment: %w", err)
	}
	return ev.validate(expr)
}

// newEvaluator creates an Evaluator backed by a CEL environment that includes
// all kro library extensions — the same extended function set as kro's Graph CEL.
// (Note: kro Graph expressions and PolicyGate expressions share the same library
// functions, but they run in separate environments and have different context variables.)
//
// Functions available in expressions:
//   - Standard strings (cel-go/ext)
//   - json.marshal(v) / json.unmarshal(s)
//   - map1.merge(map2) — a member function; there is no global maps.merge
//   - lists.setAtIndex / insertAtIndex / removeAtIndex
//   - random.seededInt(min, max, seed) / random.seededString(length, seed) (seed is a string)
//   - changewindow.isAllowed(name) → bool  (true when the named window is NOT active/blocking)
//   - changewindow.isBlocked(name) → bool  (true when the named window IS active/blocking)
//
// Context variables (populated by buildContext in reconciler.go and documented,
// with a test that holds the docs to it, in docs/reference/cel-context.md):
//   - bundle, environment, metrics, upstream, changewindow
//   - schedule — a plain map variable {isWeekend:bool, hour:int, dayOfWeek:string}
//     NOTE: schedule.* is a map injection, NOT a CEL library function.
//     It is only available here (PolicyGate CEL context), not in kro
//     Graph readyWhen/includeWhen expressions. See issue #616 and
//     docs/design/11-graph-purity-tech-debt.md §ScheduleClock Implementation.
func newEvaluator() (*evaluator, error) {
	env, err := goccel.NewEnv(
		goccel.Variable("bundle", goccel.DynType),
		goccel.Variable("schedule", goccel.DynType),
		goccel.Variable("environment", goccel.DynType),
		goccel.Variable("metrics", goccel.DynType),
		goccel.Variable("upstream", goccel.DynType),
		goccel.Variable("changewindow", goccel.DynType),
		ext.Strings(),
		library.JSON(),
		library.Maps(),
		library.Lists(),
		library.Random(),
		// changewindow.isAllowed(name) → bool
		// Returns true when the named ChangeWindow is NOT currently blocking.
		// Equivalent to: !changewindow["name"]
		// Example: changewindow.isAllowed("business-hours") — passes during business hours
		// An unknown window name is an evaluation error, so the gate fails closed.
		goccel.Function("isAllowed",
			goccel.MemberOverload(
				"changewindow_isAllowed_string",
				[]*goccel.Type{goccel.DynType, goccel.StringType},
				goccel.BoolType,
				goccel.BinaryBinding(func(mapVal ref.Val, nameVal ref.Val) ref.Val {
					active, errVal := changeWindowActive(mapVal, nameVal)
					if errVal != nil {
						return errVal
					}
					return types.Bool(!active)
				}),
			),
		),
		// changewindow.isBlocked(name) → bool
		// Returns true when the named ChangeWindow IS currently blocking.
		// Equivalent to: changewindow["name"]
		// Example: !changewindow.isBlocked("holiday-freeze") — passes when freeze is not active
		// An unknown window name is an evaluation error, so the gate fails closed.
		goccel.Function("isBlocked",
			goccel.MemberOverload(
				"changewindow_isBlocked_string",
				[]*goccel.Type{goccel.DynType, goccel.StringType},
				goccel.BoolType,
				goccel.BinaryBinding(func(mapVal ref.Val, nameVal ref.Val) ref.Val {
					active, errVal := changeWindowActive(mapVal, nameVal)
					if errVal != nil {
						return errVal
					}
					return types.Bool(active)
				}),
			),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("cel.NewEnv: %w", err)
	}
	return &evaluator{
		env:     env,
		cache:   make(map[string]goccel.Program),
		timeout: celEvalTimeout,
	}, nil
}

// changeWindowActive looks up a ChangeWindow by name in the changewindow context
// map. It returns a CEL error value when the context is not a map or the name is
// not a ChangeWindow that exists in the cluster: a typo or a deleted window must
// block the gate, not silently allow it (C04-gates-31).
func changeWindowActive(mapVal, nameVal ref.Val) (bool, ref.Val) {
	name, ok := nameVal.Value().(string)
	if !ok {
		return false, types.NewErr("changewindow: window name must be a string, got %s", nameVal.Type().TypeName())
	}
	cwMap, ok := mapVal.Value().(map[string]interface{})
	if !ok {
		return false, types.NewErr("changewindow: context is not available")
	}
	raw, found := cwMap[name]
	if !found {
		return false, types.NewErr("changewindow: unknown ChangeWindow %q", name)
	}
	active, ok := raw.(bool)
	if !ok {
		return false, types.NewErr("changewindow: ChangeWindow %q has no active state", name)
	}
	return active, nil
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
func (e *evaluator) evaluate(ctx context.Context, expr string, vars map[string]interface{}) (bool, string, error) {
	prg, err := e.getOrCompile(expr)
	if err != nil {
		return false, fmt.Sprintf("CEL compile error: %s", err), err
	}

	evalCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	out, _, err := prg.ContextEval(evalCtx, vars)
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
	return ev.evaluate(context.Background(), expr, ctx)
}

// getOrCompile returns the compiled program for expr, compiling it with the
// cost limit and interrupt checks on a cache miss.
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

	prg, err := e.env.Program(ast,
		goccel.CostLimit(celCostLimit),
		goccel.InterruptCheckFrequency(celInterruptCheckFrequency),
	)
	if err != nil {
		return nil, fmt.Errorf("cel.Program: %w", err)
	}

	if len(e.cache) >= maxCachedPrograms {
		clear(e.cache)
	}
	e.cache[expr] = prg
	return prg, nil
}
