# Spec: Bundle history GC — enforce historyLimit

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future — Lens 1: Production security and operations gap analysis`
- **Implements**: "Bundle history GC — `historyLimit` is defined in the API but never enforced" (🔲 → ✅)

## Zone 1 — Obligations (falsifiable)

O1. After each Bundle reconcile, the bundle reconciler MUST enforce `Pipeline.spec.historyLimit`
    for terminal Bundles (`Verified`, `Failed`, `Superseded`) belonging to that pipeline.
    Violation: >N terminal bundles exist for a pipeline where N=historyLimit (default 50).

O2. When `Pipeline.spec.historyLimit == 0` (unset/zero), the reconciler MUST use the default
    limit of 50. Violation: unlimited Bundles accumulate.

O3. Bundles MUST be deleted oldest-first (by CreationTimestamp). Violation: a newer terminal
    Bundle is deleted while an older one remains.

O4. Only terminal Bundles (`Verified`, `Failed`, `Superseded`) are eligible for GC.
    Violation: an `Available` or `Promoting` Bundle is deleted by GC.

O5. The GC runs in the bundle reconciler `handleNew` phase — triggered when a new Bundle
    is created. This ensures GC happens at the natural write boundary.
    Violation: GC runs in a separate goroutine or on every reconcile loop regardless of phase.

O6. GC is idempotent: running it twice produces the same result as running it once.
    Violation: a Bundle is double-deleted or an error is returned on second run.

O7. A table-driven unit test covers: (a) historyLimit enforced, (b) default limit of 50
    applied when unset, (c) non-terminal Bundles not deleted, (d) oldest-first ordering.

## Zone 2 — Implementer's judgment

- Whether to use a dedicated `enforceHistoryLimit` function or inline the logic.
- Exact list ordering algorithm (sort by CreationTimestamp, then name for stability).
- Whether to delete in a single pass or return early if count ≤ limit.
- Whether to log at Debug or Info level for each deletion.

## Zone 3 — Scoped out

- Cross-type GC (e.g. limiting total bundles regardless of type): not in scope.
- Per-environment GC of PromotionSteps: not in scope (they are owned by Graph).
- UI/CLI representation of historyLimit: not in scope.
- GC triggered by Pipeline deletion: not in scope (existing orphan guard handles that).
