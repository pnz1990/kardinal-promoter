# Feature Specification: Promotion Steps Engine

**Feature Branch**: `008-promotion-steps-engine`
**Created**: 2026-04-09
**Status**: Draft
**Depends on**: 001-graph-integration, 003-promotionstep-reconciler
**Design doc**: `docs/design/08-promotion-steps-engine.md`
**Contributes to journey(s)**: J1, J2, J4, J5 (steps engine executes all promotions)
**Constitution ref**: `.specify/memory/constitution.md`

---

## Context

The steps engine is the runtime inside the PromotionStep reconciler. It executes the step sequence (built-in or custom webhook steps), passes outputs between steps, and infers the default sequence from the environment config. This spec covers the `pkg/steps/` package and all 10 built-in step implementations.

---

## User Scenarios & Testing

### User Story 1 — Default step sequence is inferred from environment config (Priority: P1)

When `spec.steps` is omitted, the correct sequence is inferred from `update.strategy` and `approval`.

**Independent Test**: `go test ./pkg/steps/... -run TestDefaultInference`

**Acceptance Scenarios**:

1. **Given** `strategy: kustomize` + `approval: auto`, **When** `InferDefaultSteps()`, **Then** sequence is: git-clone, kustomize-set-image, git-commit, git-push, health-check
2. **Given** `strategy: kustomize` + `approval: pr-review`, **When** `InferDefaultSteps()`, **Then** sequence adds open-pr, wait-for-merge before health-check
3. **Given** `type: config` Bundle, **When** `InferDefaultSteps()`, **Then** config-merge replaces kustomize-set-image

---

### User Story 2 — Built-in steps are idempotent (Priority: P1)

Every built-in step re-run after a crash produces the same result.

**Independent Test**: `go test ./pkg/steps/steps/... -run TestIdempotency`

**Acceptance Scenarios**:

1. **Given** kustomize-set-image already applied, **When** re-run, **Then** succeeds as no-op
2. **Given** git-commit with no uncommitted changes, **When** run, **Then** succeeds as no-op
3. **Given** open-pr when PR already exists for the branch, **When** run, **Then** returns existing PR URL

---

### User Story 3 — Custom webhook step (Priority: P2)

An environment with `steps: [{uses: run-tests, config: {url: ..., timeout: 5m}}]` dispatches an HTTP POST to the configured URL.

**Acceptance Scenarios**:

1. **Given** a custom step with `url` returning `{"success": true}`, **When** executed, **Then** step succeeds and outputs are passed to next step
2. **Given** a custom step URL returning non-2xx, **When** executed, **Then** step returns StepFailed
3. **Given** a custom step timing out, **When** executed, **Then** step returns StepFailed with timeout reason

---

## Requirements

- **FR-001**: `Step` interface: `Execute(ctx, *StepState) (StepResult, error)` + `Name() string`
- **FR-002**: 10 built-in steps: git-clone, kustomize-set-image, helm-set-image, kustomize-build, config-merge, git-commit, git-push, open-pr, wait-for-merge, health-check
- **FR-003**: `InferDefaultSteps(env, bundleType)` returns correct sequence
- **FR-004**: Custom steps: HTTP POST to `config.url`, parse `StepResponse`
- **FR-005**: Outputs from each step merged into `StepState.Outputs` for downstream steps

### Package Structure

```
pkg/steps/
  engine.go         # Engine.RunStep(), step dispatch
  registry.go       # Built-in step registry
  step.go           # Step interface, StepState, StepResult, StepStatus
  state.go          # StepState and output accumulator
  defaults.go       # InferDefaultSteps()
  webhook.go        # Custom step HTTP dispatcher
  steps/
    git_clone.go    kustomize.go  helm.go  config_merge.go
    git_commit.go   git_push.go   open_pr.go
    wait_for_merge.go  health_check.go  kustomize_build.go
  steps_test.go     # 17 unit test cases
```

---

## Success Criteria

- **SC-001**: `go test ./pkg/steps/... -race` passes
- **SC-002**: All 17 unit test cases pass
- **SC-003**: Apache 2.0 header on every .go file
