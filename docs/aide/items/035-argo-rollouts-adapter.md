# Item: 035-argo-rollouts-adapter

> dependency_mode: merged
> depends_on: 014-health-adapters

## Summary

Implement `ArgoRolloutsAdapter` in `pkg/health/` for `health.type: argoRollouts`. Currently AutoDetector silently falls back to DeploymentAdapter when argoRollouts is configured, causing health checks to hang forever.

## GitHub Issue

#118 — feat(health): implement argoRollouts health adapter

## Acceptance Criteria

- `ArgoRolloutsAdapter` reads `argoproj.io/v1alpha1/Rollout` resource
- Returns `Healthy` when `status.phase == Healthy` (stable rollout)
- Returns `Healthy: false, Reason: "Rollout phase: <phase>"` otherwise  
- `AutoDetector.Select()` handles `"argoRollouts"` case explicitly
- Unit tests: Rollout Healthy → adapter returns Healthy; Rollout Degraded → returns false
- `go test ./pkg/health/... -race` passes
- Documented in docs/health-adapters.md

## Files to modify

- `pkg/health/adapter.go` (add ArgoRolloutsAdapter + AutoDetector case)
- `pkg/health/health_test.go` (add tests)
- `docs/health-adapters.md` (add argoRollouts section)

## Size: L
