# Item 022: Automatic Rollback on Health Failure (Stage 13 partial)

> **Queue**: queue-010
> **Branch**: `022-auto-rollback`
> **Depends on**: 013 (PromotionStep reconciler, merged), 014 (health adapters, merged)
> **Dependency mode**: merged
> **Contributes to**: J4 (Rollback — automatic rollback)
> **Priority**: HIGH — completes J4 automatic rollback path

---

## Goal

Implement automatic rollback on consecutive health-check failures. When a PromotionStep
in `HealthChecking` state fails `spec.autoRollback.failureThreshold` times consecutively,
the controller automatically creates a rollback Bundle and sets the current Bundle to Failed.

---

## Deliverables

### 1. `autoRollback` field on Pipeline EnvironmentSpec

In `api/v1alpha1/pipeline_types.go`, add to `EnvironmentSpec`:
```go
// AutoRollback configures automatic rollback on repeated health-check failures.
// +optional
AutoRollback *AutoRollbackSpec `json:"autoRollback,omitempty"`
```

```go
// AutoRollbackSpec defines when to automatically trigger a rollback.
type AutoRollbackSpec struct {
    // FailureThreshold is the number of consecutive health-check failures
    // before triggering an automatic rollback. Default: 3.
    // +kubebuilder:default=3
    // +optional
    FailureThreshold int `json:"failureThreshold,omitempty"`
}
```

Regenerate DeepCopy.

### 2. `ConsecutiveHealthFailures` field on PromotionStepStatus

In `api/v1alpha1/promotionstep_types.go`, add:
```go
// ConsecutiveHealthFailures is the number of consecutive health-check failures
// for this step. Reset to 0 on success.
// +optional
ConsecutiveHealthFailures int `json:"consecutiveHealthFailures,omitempty"`
```

Regenerate DeepCopy.

### 3. Automatic rollback logic in PromotionStepReconciler

In `pkg/reconciler/promotionstep/reconciler.go`:
- In the `HealthChecking` state handler: after a health check failure, increment
  `ps.Status.ConsecutiveHealthFailures`
- If `ConsecutiveHealthFailures >= failureThreshold` (from Pipeline.spec.environments[env].autoRollback):
  - Set Bundle phase to `Failed` with reason "auto-rollback: consecutive health failures"
  - Create a rollback Bundle: same Pipeline, last verified images from previous Bundle
    (or images from `Bundle.spec.images` reversed by creation timestamp)
  - Set rollback Bundle label `kardinal.io/rollback: "true"` and
    provenance field `rollbackOf: <original bundle name>`
  - Log: `auto-rollback: created rollback bundle <name>`
- This is idempotent: check if a rollback Bundle already exists before creating

### 4. Unit tests

- `TestAutoRollback_TriggersAfterThreshold`: inject 3 consecutive health failures → verify rollback Bundle created
- `TestAutoRollback_DoesNotTriggerBeforeThreshold`: 2 failures → no rollback Bundle
- `TestAutoRollback_Idempotent`: run reconcile twice after threshold → only one rollback Bundle

---

## Acceptance Criteria

- [ ] `Pipeline.spec.environments[].autoRollback.failureThreshold` field exists and is validated
- [ ] `PromotionStep.status.consecutiveHealthFailures` increments on each failure
- [ ] After `failureThreshold` consecutive failures, a rollback Bundle is created automatically
- [ ] Rollback Bundle has `kardinal.io/rollback: "true"` label and `spec.provenance.rollbackOf` set
- [ ] No duplicate rollback Bundles created (idempotent)
- [ ] `go build ./...` passes
- [ ] `go test ./... -race` passes
- [ ] `go vet ./...` passes
- [ ] Copyright headers on all new files
- [ ] No banned filenames
