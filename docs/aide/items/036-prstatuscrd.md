# Item: 036-prstatuscrd

> dependency_mode: merged
> depends_on: 012-scm-and-steps-engine, 013-promotionstep-reconciler

## Summary

Introduce a `PRStatus` CRD that makes the PR merge signal observable via the krocodile Graph, eliminating the polling-based `handleWaitingForMerge()` and the wait-for-merge step.

## GitHub Issue

#133 — arch(scm): introduce PRStatus CRD to make PR merge signal observable by Graph

## Acceptance Criteria

- `PRStatus` CRD with: `spec.prURL`, `spec.prNumber`, `spec.repo`; `status.merged` (bool), `status.open` (bool), `status.lastCheckedAt`
- `PRStatusReconciler` polls GitHub API via `GetPRStatus` and patches `status.merged/open`
- Graph builder adds `PRStatus` Watch node to the graph with `propagateWhen: ${prStatus.status.merged == true}`
- `PromotionStep.spec` gains a `prStatusRef` field pointing to the Watch node
- Existing `handleWaitingForMerge()` in promotionstep reconciler is removed/deprecated
- Idempotent: creating PRStatus twice for same PR is a no-op
- Tests: PRStatusReconciler unit test, Graph builder test including Watch node

## Files to modify

- `api/v1alpha1/prstatus_types.go` (create)
- `pkg/reconciler/prstatus/` (create)
- `pkg/graph/builder.go` (add Watch node for PRStatus)
- `pkg/reconciler/promotionstep/reconciler.go` (deprecate handleWaitingForMerge)
- `config/crd/` (regenerate)
- Tests updated

## Size: L
