# Tasks: 506-ui-actions

## Tasks

### Backend
- [ ] Add `POST /api/v1/ui/pause` handler — writes `spec.paused=true` on Pipeline CRD
- [ ] Add `POST /api/v1/ui/resume` handler — writes `spec.paused=false` on Pipeline CRD
- [ ] Add `PATCH /api/v1/ui/gates/{name}/override` handler — appends to `spec.overrides[]`
- [ ] Add `DELETE /api/v1/ui/steps/{name}` handler — deletes PromotionStep CRD
- [ ] Add integration tests for all 4 new handlers

### Frontend
- [ ] Add `api.pause()`, `api.resume()`, `api.overrideGate()`, `api.restartStep()` to `client.ts`
- [ ] Create `ActionButton.tsx` — button with loading spinner and inline error
- [ ] Create `ConfirmDialog.tsx` — modal with title, message, confirm/cancel
- [ ] Create `OverrideModal.tsx` — reason textarea (min 10 chars), expiry selector, confirm
- [ ] Wire Pause/Resume/Rollback buttons into `StageDetailPanel.tsx`
- [ ] Wire Override button into `GateDetailPanel.tsx`
- [ ] Wire Restart Step button into step list in `StageDetailPanel.tsx`
- [ ] Optimistic UI + 2s re-poll after action
- [ ] Write vitest tests for ActionButton, ConfirmDialog, OverrideModal
