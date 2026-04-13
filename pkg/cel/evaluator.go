// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cel

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"
)

// Evaluator wraps a cel.Env and provides cached CEL expression evaluation.
// All evaluation errors are fail-closed: a failing or erroring expression
// returns (false, reason, err) — it does not permit the gate to pass.
//
// The program cache is keyed by expression string. Cache is valid for the
// lifetime of the Evaluator (reset on controller restart).
type Evaluator struct {
	env   *cel.Env
	cache map[string]cel.Program
	mu    sync.Mutex
}

// NewEvaluator creates a new Evaluator backed by the given CEL environment.
func NewEvaluator(env *cel.Env) *Evaluator {
	return &Evaluator{
		env:   env,
		cache: make(map[string]cel.Program),
	}
}

// Evaluate compiles (or retrieves from cache) and evaluates the CEL expression
// against the provided context map.
//
// Returns:
//   - pass: true if the expression evaluates to true
//   - reason: human-readable explanation of the result
//   - err: non-nil if compilation or evaluation failed (implies pass=false)
//
// All errors are fail-closed: the gate does not pass on any error.
func (e *Evaluator) Evaluate(expr string, ctx map[string]interface{}) (bool, string, error) {
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

// Validate compiles the CEL expression and returns a non-nil error if it has
// a syntax or type error. It does NOT evaluate the expression — only checks
// that the expression is valid CEL and would not crash at compile time.
//
// This is safe to call for template PolicyGates without a real bundle context
// (Issue #315 — syntax-only check, not evaluation).
func (e *Evaluator) Validate(expr string) error {
	_, err := e.getOrCompile(expr)
	return err
}

// getOrCompile returns a cached program for the expression, or compiles it.
func (e *Evaluator) getOrCompile(expr string) (cel.Program, error) {
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
