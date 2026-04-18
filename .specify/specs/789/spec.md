# Spec: fix(reconciler): PromotionStep cycles Verified→Promoting — terminal state guard

## Design reference
- **Design doc**: `docs/design/03-promotion-step-reconciler.md`
- **Section**: `## Present` / state machine
- **Implements**: Fix cycling regression in PromotionStep state machine — Verified and other terminal states must not restart the promotion sequence.

---

## Zone 1 — Obligations (falsifiable)

O1. A PromotionStep in `Verified` state MUST NOT transition to any other state via the normal reconcile path. Reconciling a Verified step MUST return `ctrl.Result{}` with no requeue and no status mutation.

O2. A PromotionStep in `Failed` state MUST NOT transition to any other state via the normal reconcile path. Same as O1.

O3. A PromotionStep in `AbortedByAlarm` state MUST NOT be reset to Pending by the default case. `AbortedByAlarm` is a terminal human-intervention state. The reconciler MUST return `ctrl.Result{}` with no requeue and no status mutation.

O4. A PromotionStep in `RollingBack` state MUST NOT be reset to Pending by the default case. `RollingBack` is a managed state set by applyHealthFailurePolicy; the reconciler MUST return `ctrl.Result{}` no-op.

O5. The default case MUST only fire for genuinely unknown state values. It MUST NOT fire for AbortedByAlarm or RollingBack.

O6. A unit test MUST verify that reconciling an AbortedByAlarm step returns ctrl.Result{} with no requeue and no status mutation.

O7. A unit test MUST verify that reconciling a RollingBack step returns ctrl.Result{} with no requeue and no status mutation.

---

## Zone 2 — Implementer's judgment

- Add AbortedByAlarm and RollingBack to the existing terminal-states case or create a separate case.
- Whether to call cleanWorkDir for AbortedByAlarm/RollingBack (preferred: yes).

---

## Zone 3 — Scoped out

- Root cause of what resets status.state in production.
- propagateWhen self-reference fix (separate concern).
- Changes to CRD types or enum validation.
