# Spec: 833-cadence-reduction

> chore(ci): reduce scheduled cadence from hourly to 6h (steady-state standby)

## Design reference
- **Design doc**: `docs/design/13-scheduled-execution.md`
- **Section**: `§ Future`
- **Implements**: Reduce cadence to `0 */6 * * *` when project enters steady-state standby (🔲 → ✅)

---

## Zone 1 — Obligations

**O1**: `.github/workflows/otherness-scheduled.yml` cron expression MUST be `0 */6 * * *`.
- Violation: any other schedule expression in the `schedule:` trigger block.

**O2**: `workflow_dispatch` trigger MUST remain present after the change.
- Violation: `workflow_dispatch:` absent from the `on:` block.

**O3**: Comment in the workflow MUST reflect the new cadence.
- Violation: comment still reads "run every hour" without update.

---

## Zone 2 — Implementer's judgment

- Whether to add a comment explaining the cadence change (recommended but not required).
- Whether to update `otherness-config.yaml` if a `schedule.cron` field exists there.

---

## Zone 3 — Scoped out

- Auto-switching cadence based on queue depth (future feature).
- Updating multiple other workflow files — only `otherness-scheduled.yml` is in scope.
- Any changes to Go code, CRDs, or UI.
