# Item: 038-rollbackpolicycrd

> dependency_mode: merged
> depends_on: 013-promotionstep-reconciler, 022-auto-rollback

## Summary

Introduce a `RollbackPolicy` CRD that makes the auto-rollback decision observable via the krocodile Graph, eliminating the invisible threshold comparison and the cross-reconciler Bundle creation in `maybeCreateAutoRollback()`.

## GitHub Issue

#134 — arch(rollback): auto-rollback threshold comparison and Bundle creation happen outside Graph

## Acceptance Criteria

- `RollbackPolicy` CRD with: `spec.failureThreshold` (int), `spec.pipelineName`, `spec.environment`; `status.shouldRollback` (bool), `status.consecutiveFailures` (int), `status.lastEvaluatedAt`
- `RollbackPolicyReconciler` watches PromotionStep `status.consecutiveHealthFailures` and writes `status.shouldRollback = true` when threshold exceeded
- The threshold comparison is done in the reconciler as a CRD status write (Graph-first: reconciler writes own CRD status)
- When `shouldRollback = true`, a Watch node or the Bundle reconciler creates the rollback Bundle via the normal Graph path
- `maybeCreateAutoRollback()` in `promotionstep/reconciler.go` is removed; auto-rollback now flows through the RollbackPolicy CRD
- Idempotent: evaluating an already-triggered RollbackPolicy is a no-op
- Tests: RollbackPolicyReconciler unit test, idempotency test

## Files to modify

- `api/v1alpha1/rollbackpolicy_types.go` (create)
- `pkg/reconciler/rollbackpolicy/` (create)
- `pkg/reconciler/promotionstep/reconciler.go` (remove maybeCreateAutoRollback)
- `config/crd/` (deepcopy update)
- `cmd/kardinal-controller/main.go` (register reconciler)
- Tests updated

## Size: L
