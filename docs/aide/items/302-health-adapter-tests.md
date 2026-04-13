# Item 302: Health Adapter Test Coverage for issue #407

> Issue: #407
> Queue: queue-015
> Milestone: v0.6.0-proof
> Size: m
> Priority: high
> Area: area/health
> Kind: kind/enhancement
> Depends on: 014-health-adapters (merged)

## Context

Issue #407 identified gaps in health adapter test coverage:
- ArgoCD Application not found → no test
- Flux Kustomization not found → no test
- ArgoCD with sync OutOfSync → no test
- ArgoCD with operation in progress (opPhase=Running) → no test

## Spec Reference

`pkg/health/adapter.go`, `docs/health-adapters.md`

## Acceptance Criteria

### AC1: ArgoCD adapter covers not-found and OutOfSync cases
**Given** an ArgoCDAdapter with no Application in the cluster
**Then** result.Healthy = false, result.Reason contains "not found"

**Given** an ArgoCDAdapter with Application health=Healthy but sync=OutOfSync
**Then** result.Healthy = false

### AC2: Flux adapter covers not-found case
**Given** a FluxAdapter with no Kustomization in the cluster
**Then** result.Healthy = false, result.Reason contains "not found"

### AC3: ArgoCD adapter with active operation (opPhase=Running) blocks promotion
**Given** an ArgoCDAdapter with Application health=Healthy, sync=Synced, opPhase=Running
**Then** result.Healthy = false (operation in progress, wait for it to complete)

## Tasks

- [x] Add TestArgoCDAdapter_NotFound
- [x] Add TestArgoCDAdapter_OutOfSync
- [x] Add TestArgoCDAdapter_OperationInProgress
- [x] Add TestFluxAdapter_NotFound
- [x] Run go test ./pkg/health/... -race — all pass
- [x] Post issue #407 evidence
