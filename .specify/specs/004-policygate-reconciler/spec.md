# Feature Specification: PolicyGate Reconciler

**Feature Branch**: `004-policygate-reconciler`
**Created**: 2026-04-09
**Status**: Draft
**Depends on**: 001-graph-integration
**Design doc**: `docs/design/04-policygate-reconciler.md`
**Constitution ref**: `.specify/memory/constitution.md`

---

## Context

The PolicyGate reconciler evaluates CEL expressions on PolicyGate instances (created by Graph controller) and writes `status.ready` and `status.lastEvaluatedAt`. It is the governance engine: org gates that evaluate to false block all downstream promotion steps. Fail-closed: any evaluation error sets `status.ready = false`.

---

## User Scenarios & Testing

### User Story 1 — Weekend gate blocks production (Priority: P1)

A `no-weekend-deploys` gate with expression `!schedule.isWeekend` evaluates to false on Saturday and blocks the prod PromotionStep.

**Independent Test**: `go test ./pkg/reconciler/policygate/... -run TestWeekendGate`

**Acceptance Scenarios**:

1. **Given** a PolicyGate with `expression: "!schedule.isWeekend"` and system clock is Saturday, **When** reconciler evaluates, **Then** `status.ready = false`, reason contains "schedule.isWeekend = true"
2. **Given** same gate on a Tuesday, **When** reconciler evaluates, **Then** `status.ready = true`
3. **Given** any PolicyGate evaluation, **When** it completes, **Then** `status.lastEvaluatedAt` is set to now()

---

### User Story 2 — CEL errors fail closed (Priority: P1)

Syntax errors, unknown attributes, and non-boolean results all set `status.ready = false`.

**Independent Test**: `go test ./pkg/reconciler/policygate/... -run TestCELErrors`

**Acceptance Scenarios**:

1. **Given** a PolicyGate with a CEL syntax error, **When** reconciled, **Then** `status.ready = false`, reason contains "CEL compile error"
2. **Given** `expression: "metrics.successRate >= 0.99"` in Phase 1 (metrics not available), **When** reconciled, **Then** `status.ready = false`, reason contains "unknown attribute"
3. **Given** `expression: "bundle.version"` (non-boolean), **When** reconciled, **Then** `status.ready = false`, reason contains "non-boolean"

---

### User Story 3 — Timer-based re-evaluation (Priority: P1)

Gates re-evaluate at `recheckInterval` so time-based gates unblock without external triggers.

**Independent Test**: `go test ./pkg/reconciler/policygate/... -run TestRecheckInterval`

**Acceptance Scenarios**:

1. **Given** `recheckInterval: 1s`, **When** 1 second passes after evaluation, **Then** the reconciler re-evaluates and updates `lastEvaluatedAt`
2. **Given** a gate evaluating a stale `lastEvaluatedAt` (controller restarted), **When** Graph checks `readyWhen`, **Then** the freshness check fails and Graph does not advance until re-evaluated

---

### Edge Cases

- Template PolicyGate (no `kardinal.io/bundle` label): reconciler skips it silently
- Bundle not found: `status.ready = false`, retry at recheckInterval
- Phase 2 attributes referenced in Phase 1: fail closed with clear reason

---

## Requirements

- **FR-001**: Evaluate CEL expression against `pkg/cel/` context builder
- **FR-002**: Write `status.ready` and `status.lastEvaluatedAt` on every evaluation
- **FR-003**: MUST fail closed on any CEL error (compile, eval, type)
- **FR-004**: Return `ctrl.Result{RequeueAfter: recheckInterval}` for timer-based re-evaluation
- **FR-005**: Skip PolicyGates without `kardinal.io/bundle` label (templates, not instances)
- **FR-006**: Phase 1 context attributes: bundle.*, schedule.*, environment.*

### Go Package Structure

```
pkg/reconciler/policygate/
  reconciler.go       # Reconcile() entrypoint
  context.go          # CEL context building per phase
  evaluator.go        # CEL environment setup, compile cache, evaluation
  reconciler_test.go  # 11 unit test cases
pkg/cel/
  environment.go      # Shared CEL env registration
  types.go            # Context attribute type definitions
```

---

## Success Criteria

- **SC-001**: `go test ./pkg/reconciler/policygate/... -race` passes
- **SC-002**: All 11 unit test cases pass
- **SC-003**: CEL environment is created once at startup and reused (not per evaluation)
- **SC-004**: Apache 2.0 header on every .go file
