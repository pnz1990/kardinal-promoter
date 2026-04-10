# Feature Specification: Health Adapters

**Feature Branch**: `005-health-adapters`
**Created**: 2026-04-09
**Status**: Draft
**Depends on**: 001-graph-integration
**Design doc**: `docs/design/05-health-adapters.md`
**Contributes to journey(s)**: J1, J2 (health verification determines when promotion is complete)
**Constitution ref**: `.specify/memory/constitution.md`

---

## Context

Health adapters verify that a GitOps tool successfully applied promoted manifests. The `health-check` step calls the appropriate adapter during HealthChecking. Phase 1: Deployment, Argo CD, Flux. Phase 2: Argo Rollouts, Flagger. Auto-detected from installed CRDs. Remote clusters via kubeconfig Secrets.

---

## User Scenarios & Testing

### User Story 1 — Argo CD Application health auto-detected (Priority: P1)

When Argo CD is installed, the controller auto-detects it and uses the `argocd` adapter without configuration.

**Independent Test**: `go test ./pkg/health/... -run TestArgoCDAutoDetect`

**Acceptance Scenarios**:

1. **Given** the Application CRD exists in the cluster, **When** `registry.AutoDetect()` runs, **Then** the `argocd` adapter is selected
2. **Given** `health.status=Healthy`, `sync.status=Synced`, `operationState.phase=Succeeded`, **When** `adapter.Check()` is called, **Then** it returns `Healthy: true`
3. **Given** `health.status=Progressing`, **When** `adapter.Check()` is called, **Then** it returns `Healthy: false` (wait)
4. **Given** `health.status=Degraded`, **When** timeout passes, **Then** PromotionStep transitions to Failed

---

### User Story 2 — Flux Kustomization health with generation matching (Priority: P1)

The Flux adapter returns Healthy only when `Ready=True` AND `observedGeneration == generation`.

**Independent Test**: `go test ./pkg/health/... -run TestFluxAdapter`

**Acceptance Scenarios**:

1. **Given** `Ready=True` and `observedGeneration == generation`, **When** `adapter.Check()`, **Then** returns Healthy
2. **Given** `Ready=True` but `observedGeneration < generation`, **When** `adapter.Check()`, **Then** returns not healthy (wait)

---

### User Story 3 — Remote cluster via kubeconfig Secret (Priority: P2)

For multi-cluster, a `cluster` field on the health config references a kubeconfig Secret in the controller namespace.

**Independent Test**: `go test ./pkg/health/... -run TestRemoteClusterClient`

**Acceptance Scenarios**:

1. **Given** a kubeconfig Secret named `prod-eu` in the controller namespace, **When** `health.cluster=prod-eu` is configured, **Then** the adapter creates a dynamic client for the remote cluster
2. **Given** the same Secret is referenced twice, **When** `GetClient()` is called, **Then** the cached client is returned (no re-parse)

---

### Edge Cases

- No GitOps tool installed: falls back to Deployment `Available` condition
- Resource not found: return unhealthy (retry — may not be synced yet)
- Suspended Kustomization/Application: return unhealthy (will fail after timeout)

---

## Requirements

- **FR-001**: Three Phase 1 adapters: `resource`, `argocd`, `flux`
- **FR-002**: Auto-detection: check CRDs at startup and every 5 minutes
- **FR-003**: Priority order: argocd > flux > resource
- **FR-004**: Remote cluster: load kubeconfig from Secret, cache client per Secret name
- **FR-005**: `Check()` MUST NOT cache results across calls

### Go Package Structure

```
pkg/health/
  adapter.go        # Adapter interface + CheckOptions + HealthStatus
  registry.go       # Registry, AutoDetect(), Get()
  resource.go       # Deployment condition adapter
  argocd.go         # Argo CD Application adapter (dynamic client)
  flux.go           # Flux Kustomization adapter (dynamic client)
  remote.go         # RemoteClientManager (kubeconfig Secret → dynamic client)
  health_test.go    # 16 unit test cases
```

---

## Success Criteria

- **SC-001**: `go test ./pkg/health/... -race` passes
- **SC-002**: All 16 unit test cases pass
- **SC-003**: No compile-time Argo CD or Flux imports — dynamic client only
- **SC-004**: Apache 2.0 header on every .go file
