# Spec: 506-ui-actions

> Feature: In-UI actions — approve, pause, rollback, override gate, restart step
> Issue: #464
> Milestone: v0.6.0 — Pipeline Expressiveness
> Graph-purity: Actions POST to kardinal API → controller writes CRD status

## Problem

The UI is read-only. Operators need to act during incidents without switching to CLI.

## Acceptance Criteria

**FR-506-01**: Stage-level actions visible on stage detail panel: Pause (when Promoting), Resume (when Paused), Rollback (when Verified or Failed).

**FR-506-02**: Pause/Resume call `POST /api/v1/ui/pause` and `POST /api/v1/ui/resume` with `{pipeline, namespace}`.

**FR-506-03**: Rollback calls existing `POST /api/v1/ui/rollback` with `{pipeline, environment, namespace}`.

**FR-506-04**: Override gate action opens a modal: requires reason text (min 10 chars), expiry selector (1h/4h/24h). Calls `PATCH /api/v1/ui/gates/{name}/override` with `{reason, expiresAt, stage}`.

**FR-506-05**: Destructive actions (rollback, override) show a confirmation dialog before executing.

**FR-506-06**: After action: optimistic state update + re-poll in 2s. Errors shown inline with the button.

**FR-506-07**: New backend endpoints required: `POST /api/v1/ui/pause`, `POST /api/v1/ui/resume`, `PATCH /api/v1/ui/gates/{name}/override`.

**FR-506-08**: Restart step: `DELETE /api/v1/ui/steps/{name}` (controller reconciler re-creates on next cycle).

## Architecture Notes

- Pause/resume: write `spec.paused` annotation on Pipeline CRD
- Override: write `spec.overrides[]` entry on PolicyGate CRD (same as `kardinal override` CLI)
- Restart step: delete PromotionStep CRD (reconciler is idempotent, re-creates on next cycle)
- No new CRDs, no new reconciler logic — pure API surface additions

## Implementation Notes

- Add 3 new handlers to `ui_api.go`: handlePause, handleResume, handleGateOverride
- Add `PATCH /api/v1/ui/steps/{name}` handler (DELETE step)
- Add corresponding `api.pause()`, `api.resume()`, `api.overrideGate()` in `client.ts`
- Create `ActionButton.tsx` — button + inline error + loading state
- Create `ConfirmDialog.tsx` and `OverrideModal.tsx`
- Wire actions into `StageDetailPanel.tsx` and `GateDetailPanel.tsx` (from 501, 502)
