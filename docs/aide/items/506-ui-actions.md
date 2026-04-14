# Item 506: In-UI Actions — approve, pause, rollback, override, restart step

> Queue: queue-018
> Issue: #464
> Priority: critical
> Size: xl
> Milestone: v0.6.0 — UI Control Plane
> Depends on: 500, 501 (pipeline ops view + stage detail — already done as PRs #475, #476)

## Summary

Add action buttons to the pipeline detail and stage detail views. Each button calls the
kardinal API. Destructive actions require confirmation dialogs. Override gate requires
a mandatory reason text field.

## Acceptance Criteria

- [ ] Stage-level actions visible on appropriate stage states:
      Pause (Promoting), Resume (Paused), Rollback (Verified/Failed), Restart step (Failed)
- [ ] Gate-level actions: Override gate (blocking gate, modal with reason field)
- [ ] Bundle-level actions at pipeline detail top: Pause pipeline, Create bundle (modal with image input)
- [ ] Override gate modal: required reason text, calls `PATCH policygate {spec.overrides}` (K-09 API)
- [ ] Destructive actions (rollback, pause) require confirmation dialog before API call
- [ ] After action: UI optimistically updates and re-polls in 2s
- [ ] Inline error display on button failure (not just toast)
- [ ] Frontend tests: each action button appears in correct state, confirmation dialog, error state
- [ ] All actions use existing API endpoints — no new backend endpoints required
      (uses: PATCH /api/v1/pipelines/{name} for pause, POST rollback, PATCH policygate for override)

## Package

`web/src/components/` — ActionButton, ConfirmDialog, OverrideGateModal, CreateBundleModal components
`web/src/` — PipelineDetail, StageDetail pages extended with action buttons

## Notes

The override gate action reuses the K-09 (`kardinal override`) backend that landed in PR #471.
No new backend endpoints needed for this item.
