# Item 014: Health Adapters — Deployment, Argo CD, Flux (Stage 7)

> **Queue**: queue-007
> **Branch**: `014-health-adapters`
> **Depends on**: 013 (merged — PromotionStep reconciler)
> **Dependency mode**: merged
> **Assignable**: immediately
> **Contributes to**: J1, J2 (health verification)
> **Priority**: HIGH — required for J1 journey to pass

---

## Goal

Replace the health-check stub with real health verification. The controller checks
Deployment readiness, Argo CD Application health+sync, or Flux Kustomization Ready —
whichever is present. The PromotionStep reconciler enters HealthChecking and advances
to Verified only when the health check passes.

Design spec: `docs/design/05-health-adapters.md`, roadmap Stage 7.

---

## Deliverables

### 1. `pkg/health` package

```
pkg/health/
  adapter.go          # Adapter interface: Check + Name + Available
  registry.go         # AutoDetector: checks CRDs on startup, selects adapter
  resource.go         # DeploymentAdapter: Deployment readiness conditions
  argocd.go           # ArgoCDAdapter: Application health=Healthy + sync=Synced
  flux.go             # FluxAdapter: Kustomization Ready condition
  health_test.go      # Unit tests (table-driven, mock k8s client)
```

**Adapter interface:**
```go
type Adapter interface {
    Check(ctx context.Context, opts CheckOptions) (HealthStatus, error)
    Name() string
    Available(ctx context.Context, discoveryClient discovery.DiscoveryInterface) (bool, error)
}

type HealthStatus struct {
    Phase   string  // "Healthy", "Degraded", "Progressing", "Unknown"
    Message string
    CheckedAt time.Time
}
```

**AutoDetector:**
- On startup and every 5 minutes, lists installed CRDs
- Priority: argocd > flux > resource (Deployment)
- Returns the highest-priority available adapter

### 2. Wire health check into PromotionStepReconciler

In `pkg/reconciler/promotionstep/reconciler.go`, replace the stub health-check step
dispatch with:
- `handleHealthChecking` calls `AutoDetector.Select()` then `adapter.Check()`
- Polls every 10 seconds until Healthy or timeout (default 10m, configurable via `Pipeline.spec.environments[].health.timeout`)
- On Healthy: set state=Verified, record healthCheckedAt
- On timeout: set state=Failed, reason="health check timeout"

### 3. Write health result to Bundle evidence

After health check passes, write `Bundle.status.environments[env].healthCheckedAt`
(already partially wired in item 013 — complete the timestamp population).

### 4. Unit tests

- `DeploymentAdapter_Healthy`: mock client returns Deployment with all pods ready
- `DeploymentAdapter_Degraded`: some pods not ready
- `ArgoCDAdapter_Healthy`: Application with health=Healthy + sync=Synced
- `ArgoCDAdapter_Degraded`: Application with health=Degraded
- `FluxAdapter_Healthy`: Kustomization with Ready condition = True
- `AutoDetector_SelectsArgoCD`: when ArgoCD CRD present, selects ArgoCDAdapter
- `AutoDetector_FallsBack`: no ArgoCD/Flux CRDs, falls back to DeploymentAdapter
- `HealthCheckStep_Timeout`: health check exceeds timeout, returns Failed

---

## Acceptance Criteria

- [ ] `Adapter` interface defined with Check/Name/Available
- [ ] `DeploymentAdapter` returns Healthy when all pods are ready, Degraded otherwise
- [ ] `ArgoCDAdapter` returns Healthy when Application health=Healthy AND sync=Synced
- [ ] `FluxAdapter` returns Healthy when Kustomization Ready condition=True
- [ ] `AutoDetector` selects adapter by priority: argocd > flux > resource
- [ ] PromotionStepReconciler `handleHealthChecking` uses the real adapter (not stub)
- [ ] Health check timeout configurable via Pipeline spec; defaults to 10 minutes
- [ ] `Bundle.status.environments[env].healthCheckedAt` set after health check passes
- [ ] `go build ./...` passes
- [ ] `go test ./... -race` passes
- [ ] `go vet ./...` passes
- [ ] Copyright headers on all new files
- [ ] No banned filenames

---

## Notes

- Use `k8s.io/client-go/discovery` for CRD availability check
- ArgoCD Application type: `argoproj.io/v1alpha1/Application`
- Flux Kustomization type: `kustomize.toolkit.fluxcd.io/v1/Kustomization`
- Use dynamic client for CRD-based resources (ArgoCD, Flux) to avoid hard dependencies
- Timeout: `Pipeline.spec.environments[].health.timeout` (Go duration string, e.g. "30m")
- The health-check step in `pkg/steps/steps/health_check.go` remains a stub;
  the reconciler handles real health via direct adapter call (not step)
