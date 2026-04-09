# Tasks: PolicyGate Reconciler

**Input**: `.specify/specs/004-policygate-reconciler/spec.md` + `docs/design/04-policygate-reconciler.md`
**Feature Branch**: `004-policygate-reconciler`
**Test command**: `go test ./pkg/reconciler/policygate/... ./pkg/cel/... -race`

---

## Phase 1: Setup

**Checkpoint**: `go build ./pkg/cel/... ./pkg/reconciler/policygate/...` succeeds.

- [ ] T001 Create `pkg/cel/environment.go`: NewCELEnvironment() registering Phase 1 context variables — file: `pkg/cel/environment.go`
- [ ] T002 [P] Create `pkg/cel/types.go`: Go types for CEL context (BundleContext, ScheduleContext, EnvironmentContext) — file: `pkg/cel/types.go`
- [ ] T003 [P] Create `pkg/reconciler/policygate/` skeleton with Apache 2.0 headers — files: `reconciler.go`, `context.go`, `evaluator.go`

---

## Phase 2: Tests First

**Checkpoint**: Tests compile but fail.

- [ ] T004 Write `TestWeekendGate_Saturday`: expression `!schedule.isWeekend`, mocked Saturday → status.ready = false — file: `pkg/reconciler/policygate/reconciler_test.go`
- [ ] T005 [P] Write `TestWeekendGate_Tuesday`: same expression, mocked Tuesday → status.ready = true — file: `pkg/reconciler/policygate/reconciler_test.go`
- [ ] T006 [P] Write `TestCELSyntaxError`: malformed expression → fail closed — file: `pkg/reconciler/policygate/reconciler_test.go`
- [ ] T007 [P] Write `TestUnknownAttribute`: `metrics.successRate` in Phase 1 → fail closed — file: `pkg/reconciler/policygate/reconciler_test.go`
- [ ] T008 [P] Write `TestNonBooleanExpression`: `bundle.version` → fail closed — file: `pkg/reconciler/policygate/reconciler_test.go`
- [ ] T009 [P] Write `TestTemplateSkipped`: PolicyGate without `kardinal.io/bundle` label → reconciler returns without processing — file: `pkg/reconciler/policygate/reconciler_test.go`
- [ ] T010 [P] Write `TestRecheckInterval`: reconcile returns `RequeueAfter` matching `spec.recheckInterval` — file: `pkg/reconciler/policygate/reconciler_test.go`
- [ ] T011 [P] Write `TestLastEvaluatedAt`: every evaluation sets `status.lastEvaluatedAt` to now() — file: `pkg/reconciler/policygate/reconciler_test.go`

---

## Phase 3: Implementation

**Checkpoint**: `go test ./pkg/reconciler/policygate/... ./pkg/cel/... -race` passes.

- [ ] T012 Implement `evaluator.go`: compile CEL expression (with cache), evaluate against context, return (bool, reason, error) — file: `pkg/reconciler/policygate/evaluator.go`
- [ ] T013 [P] Implement `context.go`: `buildContext()` assembling Phase 1 attributes from Bundle CR + system clock — file: `pkg/reconciler/policygate/context.go`
- [ ] T014 Implement `reconciler.go`: `Reconcile()` — skip templates, build context, evaluate, write status, return RequeueAfter — file: `pkg/reconciler/policygate/reconciler.go`

---

## Phase 4: Validation

- [ ] T015 Verify `go test ./pkg/reconciler/policygate/... ./pkg/cel/... -race` passes
- [ ] T016 [P] Verify `go vet` passes
- [ ] T017 [P] Apache 2.0 headers
- [ ] T018 Run `/speckit.verify-tasks.run`
