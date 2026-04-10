# Feature Specification: PromotionStep Reconciler

**Feature Branch**: `003-promotionstep-reconciler`
**Created**: 2026-04-09
**Status**: Draft
**Depends on**: 001-graph-integration, 002-pipeline-translator, 008-promotion-steps-engine
**Design doc**: `docs/design/03-promotionstep-reconciler.md`
**Contributes to journey(s)**: J1, J2, J4 (PromotionStep reconciler runs the promotion)
**Constitution ref**: `.specify/memory/constitution.md`

---

## Context

The PromotionStep reconciler watches PromotionStep CRDs created by the Graph controller and executes the promotion: running the step sequence, managing the state machine, and writing evidence to the Bundle status. This is the workhorse of the system. It runs identically in standalone and distributed (agent) modes.

---

## User Scenarios & Testing

### User Story 1 — Auto-approval environment promotes end-to-end (Priority: P1)

A PromotionStep for a `approval: auto` environment transitions from Pending → Promoting → HealthChecking → Verified and updates Bundle status with evidence.

**Independent Test**: `go test ./pkg/reconciler/promotionstep/... -run TestAutoApprovalFlow`

**Acceptance Scenarios**:

1. **Given** a PromotionStep in Pending state, **When** the reconciler runs, **Then** state transitions to Promoting and step execution begins
2. **Given** all promotion steps succeed, **When** the health adapter returns Healthy, **Then** state transitions to Verified and `verifiedAt` is set
3. **Given** a PromotionStep reaches Verified, **When** the reconciler copies evidence, **Then** `Bundle.status.environments[env]` has prURL, verifiedAt, approvedBy, and policyGates

---

### User Story 2 — PR-review environment opens PR and waits (Priority: P1)

For `approval: pr-review`, a PR is opened and the reconciler waits in WaitingForMerge until the webhook delivers a merge event.

**Independent Test**: `go test ./pkg/reconciler/promotionstep/... -run TestPRReviewFlow`

**Acceptance Scenarios**:

1. **Given** a pr-review PromotionStep, **When** `open-pr` step completes, **Then** state is WaitingForMerge and `status.prURL` is set
2. **Given** WaitingForMerge, **When** `status.prMerged = true` is set by the webhook handler, **Then** state transitions to HealthChecking
3. **Given** WaitingForMerge, **When** `status.prClosed = true` (closed without merge), **Then** state transitions to Failed

---

### User Story 3 — Health timeout triggers failure (Priority: P2)

If the health adapter does not report Healthy within the configured timeout, the PromotionStep fails.

**Independent Test**: `go test ./pkg/reconciler/promotionstep/... -run TestHealthTimeout`

**Acceptance Scenarios**:

1. **Given** `health.timeout: 1s` and an adapter that never returns Healthy, **When** 1 second passes, **Then** state transitions to Failed with reason "Health check timeout"
2. **Given** a Failed PromotionStep, **When** the reconciler runs, **Then** evidence is copied to Bundle and no further state changes occur

---

### User Story 4 — Shard filtering in distributed mode (Priority: P2)

An agent with `--shard=eu-cluster` only reconciles PromotionSteps labeled `kardinal.io/shard=eu-cluster`.

**Independent Test**: `go test ./pkg/reconciler/promotionstep/... -run TestShardFiltering`

**Acceptance Scenarios**:

1. **Given** a PromotionStep with `kardinal.io/shard=eu-cluster` and an agent with `--shard=us-cluster`, **When** reconcile is called, **Then** the reconciler returns immediately without processing
2. **Given** a PromotionStep with no shard label and standalone mode, **When** reconcile is called, **Then** it processes normally

---

### Edge Cases

- Crash mid-step: reconciler resumes from `status.currentStepIndex` (idempotent)
- Step returns conflict on git-push: retried up to 3 times
- All steps succeed but adapter never healthy: timeout → Failed → rollback triggered

---

## Requirements

- **FR-001**: State machine: Pending → Promoting → WaitingForMerge (pr-review only) → HealthChecking → Verified/Failed
- **FR-002**: `status.currentStepIndex` persists step position for crash recovery
- **FR-003**: Evidence MUST be copied to `Bundle.status.environments[env]` at Verified and Failed
- **FR-004**: Shard filtering: skip PromotionSteps with non-matching `kardinal.io/shard` label
- **FR-005**: All reconciler operations MUST be idempotent

### Go Package Structure

```
pkg/reconciler/promotionstep/
  reconciler.go       # Reconcile() entrypoint, state dispatch
  state_machine.go    # handlePending/Promoting/WaitingForMerge/HealthChecking
  evidence.go         # Evidence collection and Bundle status copy
  reconciler_test.go  # 12 unit test cases
```

---

## Success Criteria

- **SC-001**: `go test ./pkg/reconciler/promotionstep/... -race` passes
- **SC-002**: All 12 unit test cases from design doc pass
- **SC-003**: Apache 2.0 header on every .go file
- **SC-004**: Every state transition has an idempotency test
