# Tasks: Health Adapters

**Input**: `.specify/specs/005-health-adapters/spec.md` + `docs/design/05-health-adapters.md`
**Feature Branch**: `005-health-adapters`
**Test command**: `go test ./pkg/health/... -race`

---

## Phase 1: Setup

**Checkpoint**: `go build ./pkg/health/...` succeeds.

- [ ] T001 Create `pkg/health/adapter.go`: Adapter interface, CheckOptions, HealthStatus types — file: `pkg/health/adapter.go`
- [ ] T002 [P] Create `pkg/health/registry.go`: Registry struct, Get(), AutoDetect(), Available() check via CRD discovery — file: `pkg/health/registry.go`
- [ ] T003 [P] Create `pkg/health/remote.go`: RemoteClientManager with mutex, Secret loading, client cache — file: `pkg/health/remote.go`

---

## Phase 2: Tests First

**Checkpoint**: Tests compile but fail.

- [ ] T004 Write `TestResourceAdapter_Healthy`: Deployment with Available=True → Healthy — file: `pkg/health/health_test.go`
- [ ] T005 [P] Write `TestResourceAdapter_NotFound`: Deployment not found → unhealthy — file: `pkg/health/health_test.go`
- [ ] T006 [P] Write `TestArgoCDAdapter_HealthySynced`: Healthy+Synced+Succeeded → Healthy — file: `pkg/health/health_test.go`
- [ ] T007 [P] Write `TestArgoCDAdapter_Progressing`: Progressing → not healthy — file: `pkg/health/health_test.go`
- [ ] T008 [P] Write `TestArgoCDAdapter_NotFound`: Application not found → unhealthy — file: `pkg/health/health_test.go`
- [ ] T009 [P] Write `TestFluxAdapter_ReadyGenerationMatch`: Ready=True + generations match → Healthy — file: `pkg/health/health_test.go`
- [ ] T010 [P] Write `TestFluxAdapter_StaleGeneration`: Ready=True but generations differ → not healthy — file: `pkg/health/health_test.go`
- [ ] T011 [P] Write `TestAutoDetect_ArgoCD`: Application CRD present → selects argocd — file: `pkg/health/health_test.go`
- [ ] T012 [P] Write `TestAutoDetect_Fallback`: no CRDs → selects resource — file: `pkg/health/health_test.go`
- [ ] T013 [P] Write `TestRemoteClient_Cached`: second call returns cached client — file: `pkg/health/health_test.go`

---

## Phase 3: Implementation

**Checkpoint**: `go test ./pkg/health/... -race` passes.

- [ ] T014 Implement `resource.go`: Deployment condition check via typed client — file: `pkg/health/resource.go`
- [ ] T015 [P] Implement `argocd.go`: Application health+sync+operationState via dynamic client — file: `pkg/health/argocd.go`
- [ ] T016 [P] Implement `flux.go`: Kustomization Ready condition + generation match via dynamic client — file: `pkg/health/flux.go`
- [ ] T017 [P] Implement `remote.go`: load kubeconfig from Secret, create dynamic client, cache with mutex — file: `pkg/health/remote.go`
- [ ] T018 Implement `registry.go`: AutoDetect() probes CRDs at startup, caches result, refreshes every 5 minutes — file: `pkg/health/registry.go`

---

## Phase 4: Validation

- [ ] T019 Verify `go test ./pkg/health/... -race` passes
- [ ] T020 [P] Verify no Argo CD or Flux imports in go.mod (dynamic client only)
- [ ] T021 [P] Apache 2.0 headers
- [ ] T022 Run `/speckit.verify-tasks.run`
