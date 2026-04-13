# Item 303: PRStatus Regression Tests for issues #276/#277

> Issue: #408 (partial)
> Queue: queue-015
> Milestone: v0.6.0-proof
> Size: s
> Priority: high
> Area: area/test
> Kind: kind/enhancement
> Depends on: 036-prstatuscrd (merged)

## Context

Issues #276 and #277 caused critical bugs (PR merge deadlock and CRD validation
failures) that were fixed but not regression-tested. Issue #408 requires regression
tests to ensure these bugs don't reappear.

## Acceptance Criteria

### AC1: PRStatus with empty spec reconciles without error (#276 regression)
**Given** a PRStatus with no prURL, no prNumber (placeholder state from Graph)
**When** the reconciler runs
**Then** no error, requeue after pollingInterval (SCM not called — empty spec)

### AC2: PRStatus reconciler with empty spec does not call SCM (#276 regression)
**Given** a PRStatus with zero prNumber
**When** the reconciler runs
**Then** GetPRStatus is never called

## Tasks

- [x] Add TestReconciler_EmptySpec_NoSCMCall (PRStatus with empty spec)
- [x] Run go test ./pkg/reconciler/prstatus/... -race — all pass
