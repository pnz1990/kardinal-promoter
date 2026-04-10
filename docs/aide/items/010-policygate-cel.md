# Item 010: PolicyGate CEL Evaluator (Stage 4)

> **Queue**: queue-005
> **Branch**: `010-policygate-cel`
> **Depends on**: 009 (merged — Graph Builder + BundleReconciler)
> **Dependency mode**: merged
> **Assignable**: immediately
> **Contributes to**: J1, J3
> **Priority**: HIGH — Stage 6 depends on Stages 4 + 5

---

## Goal

Implement the PolicyGate CEL evaluator: `pkg/cel/` package + `PolicyGateReconciler`
in `pkg/reconciler/policygate/`.

Design spec: `docs/design/04-policygate-reconciler.md`

---

## Deliverables

### 1. `pkg/cel/environment.go`

```go
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
```

- `NewCELEnvironment() (*cel.Env, error)` — registers Phase 1 variables
  (bundle, schedule, environment) as untyped maps via `cel.MapType(cel.StringType, cel.DynType)`
  Note: use untyped maps for Phase 1 — the full ObjectType registration is Phase 2+.
  The expression compiler will accept any attribute access on maps.

### 2. `pkg/cel/evaluator.go`

```go
// Evaluator wraps a cel.Env and provides expression evaluation.
type Evaluator struct {
    env   *cel.Env
    cache map[string]cel.Program // keyed by expression string
    mu    sync.Mutex
}

func NewEvaluator(env *cel.Env) *Evaluator

// Evaluate compiles (or uses cached), evaluates, and returns (pass bool, reason string, error).
// All errors are fail-closed: returns (false, reason, err).
// Caches compiled programs by expression string (invalidated on restart).
func (e *Evaluator) Evaluate(expr string, ctx map[string]interface{}) (bool, string, error)
```

Benchmark: `Evaluate` with a 10-clause expression in under 10ms p99.

### 3. `pkg/reconciler/policygate/reconciler.go`

`PolicyGateReconciler` implementing the loop from `docs/design/04-policygate-reconciler.md`:

- Watches `PolicyGate` objects
- Skip instances without `kardinal.io/bundle` label (templates, not instances)
- Build CEL context from Bundle, system clock, gate labels
- Call `Evaluator.Evaluate`
- Patch `status.ready` + `status.lastEvaluatedAt` + `status.reason`
- Return `ctrl.Result{RequeueAfter: recheckInterval}`

**Phase 1 context only** (no Phase 2 metrics/soak):
```go
context := map[string]interface{}{
    "bundle": map[string]interface{}{
        "version":    extractVersion(bundle),     // images[0].tag or configRef.commitSHA[:8]
        "type":       bundle.Spec.Type,
        "provenance": map[string]interface{}{
            "author":    bundle.Spec.Provenance.Author,
            "commitSHA": bundle.Spec.Provenance.CommitSHA,
            "ciRunURL":  bundle.Spec.Provenance.CIRunURL,
        },
        "intent": map[string]interface{}{
            "targetEnvironment": intentTargetEnv(bundle),
        },
    },
    "schedule": map[string]interface{}{
        "isWeekend": isWeekend(now),
        "hour":      now.Hour(),
        "dayOfWeek": now.Weekday().String(),
    },
    "environment": map[string]interface{}{
        "name": gate.Labels["kardinal.io/environment"],
    },
}
```

Bundle lookup: gate has `kardinal.io/bundle` label → look up Bundle in same namespace.

### 4. Wire reconciler into controller manager

In `cmd/kardinal-controller/main.go`, add:
```go
celEnv, err := celpkg.NewCELEnvironment()
// handle err
celEval := celpkg.NewEvaluator(celEnv)
if err := (&policygaterecon.Reconciler{
    Client:    mgr.GetClient(),
    Evaluator: celEval,
}).SetupWithManager(mgr); err != nil {
    logger.Fatal().Err(err).Msg("unable to set up PolicyGateReconciler")
}
```

### 5. Unit tests

In `pkg/reconciler/policygate/reconciler_test.go` and `pkg/cel/evaluator_test.go`:

1. Weekend gate: `!schedule.isWeekend` → true on weekday, false on weekend
2. Author gate: `bundle.provenance.author != "dependabot[bot]"` → pass/fail
3. Soak time gate: `bundle.upstreamSoakMinutes >= 30` — Phase 1 only: test that attribute is unknown and fails closed (soak is Phase 2)
4. CEL syntax error → fail-closed with reason
5. Unknown attribute → fail-closed
6. Non-boolean expression → fail-closed
7. Bundle not found → fail-closed
8. Template ignored (no `kardinal.io/bundle` label) → no-op
9. `ctrl.Result.RequeueAfter` matches gate's `recheckInterval`
10. Status correctly patched: `ready`, `reason`, `lastEvaluatedAt`
11. Config Bundle type available in context
12. Idempotency: reconciling same gate twice produces same result

Benchmark test: `Evaluator.Evaluate` with 10-clause expression, assert p99 < 10ms over 1000 iterations.

---

## Acceptance Criteria

- [ ] `pkg/cel/environment.go`: `NewCELEnvironment()` registers Phase 1 variables
- [ ] `pkg/cel/evaluator.go`: `Evaluator.Evaluate` — cache, fail-closed errors, benchmark
- [ ] `PolicyGateReconciler`: evaluates CEL, patches status, requeues after recheckInterval
- [ ] Template vs instance distinction (skip templates without bundle label)
- [ ] All errors fail-closed
- [ ] 12 unit tests pass under `-race`
- [ ] Benchmark: `Evaluate` p99 < 10ms (1000 iterations)
- [ ] `go build ./...`, `go test ./... -race`, `go vet ./...` all pass
- [ ] Copyright headers on all new files
- [ ] No banned filenames
- [ ] Reconciler is idempotent

---

## Anti-patterns to Avoid

- Do NOT use `fmt.Println` — use `zerolog.Ctx(ctx)`
- Do NOT use bare errors — wrap with `fmt.Errorf("context: %w", err)`
- Do NOT register CEL ObjectTypes in Phase 1 — use map types
- Do NOT store state in the reconciler — reconcilers must be re-runnable after crash

---

## Notes

- `google/cel-go` is already in `go.mod`
- Use `sync.Mutex` for cache safety (no goroutine per gate)
- `recheckInterval` is a Go duration string (e.g. "5m") — use `time.ParseDuration`
- Bundle label on gate instance: `kardinal.io/bundle` — this was set by the graph builder
