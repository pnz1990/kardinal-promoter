# 20: Rollback and Pause/Resume

> Status: Complete | Created: 2026-04-22
> See also: `pkg/reconciler/promotionstep/`, `cmd/kardinal/`

---

## What this does

Rollback opens a revert PR targeting the previous Bundle version's image. Pause/Resume allows operators to temporarily halt promotion without deleting the Bundle.

---

## Present (✅)

- ✅ **Rollback flow**: `kardinal rollback <pipeline> --env <env>` → looks up the last `Verified` Bundle for that environment → opens a PR with the previous image → PR labeled `kardinal/rollback`.
- ✅ **Rollback PR evidence**: PR body includes current image, target image, reason, and audit trail.
- ✅ **Pause**: `Bundle.spec.paused = true` → reconciler stops advancing PromotionSteps → PAUSED badge in UI.
- ✅ **Resume**: `Bundle.spec.paused = false` → reconciler resumes from current phase.
- ✅ **Pause visibility**: `kardinal get pipelines` shows `PAUSED` badge when `spec.paused=true`.
- ✅ **Idempotency**: rollback is safe to retry — second call detects existing rollback PR and returns its URL.

---

## Future (🔲)

- 🔲 **Rollback to arbitrary version**: `--to-bundle <name>` flag to specify rollback target. Not scheduled.
- 🔲 **Automatic rollback on sustained health failure**: configurable `rollbackOnHealthFailure: true` in Pipeline spec.

---

## Zone 1 — Obligations

**O1** — Rollback PR has `kardinal/rollback` label applied at creation.
**O2** — Pausing a Bundle does not affect other Bundles in the same Pipeline.
**O3** — Rollback is idempotent: no duplicate PRs opened on repeated calls.
