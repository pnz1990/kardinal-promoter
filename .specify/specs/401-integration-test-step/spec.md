# Spec: Integration Test Step — Kubernetes Job as Promotion Step (K-07)

> Feature ID: 401-integration-test-step
> Issue: #449
> Milestone: v0.6.0 — Pipeline Expressiveness
> Status: Implemented

## Background

Integration tests running against the deployed service are the most valuable
quality gate after bake time. Built-in integration-test step creates a
batch/v1 Job, watches completion, writes result to step output accumulator.

## Functional Requirements

- FR-001: Built-in step name "integration-test" registered in step engine
- FR-002: Creates batch/v1 Job with deterministic name (prevents duplicates)
- FR-003: Idempotent — gets existing Job rather than re-creating
- FR-004: Returns StepPending + RequeueAfter:15s when Job is running
- FR-005: Returns StepSucceeded when Job.status.succeeded >= 1
- FR-006: Returns StepFailed when Job.status.failed > 0
- FR-007: Configurable timeout (integration_test.timeout, default 30m)
- FR-008: Required image (integration_test.image)
- FR-009: Optional command (integration_test.command, space-separated)
- FR-010: K8sClient field added to StepState; populated in reconciler

## Acceptance Criteria

- AC-001: Creates Job on first call, returns Pending
- AC-002: Returns Succeeded on Job succeeded >= 1
- AC-003: Returns Failed on Job failed > 0
- AC-004: Does not create duplicate on second call (idempotent)
- AC-005: Returns Failed on timeout exceeded
- AC-006: Returns Failed on missing image
- AC-007: Returns Failed on nil K8sClient
