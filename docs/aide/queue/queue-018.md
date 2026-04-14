# Queue 018 — v0.6.0 UI: Control Plane Features

> Created: 2026-04-14
> Status: Active
> Purpose: v0.6.0 — UI features that turn kardinal-ui into a control plane, not a status display

## Items

| Item | Issue | Title | Priority | Size | Depends on |
|---|---|---|---|---|---|
| 500-ui-pipeline-ops-view | #462 | Pipeline list operations view — sortable health columns | high | m | — |
| 501-ui-stage-detail | #463 | Per-stage approval workflow detail — step states, bake countdown | high | l | — |
| 502-ui-gate-detail | #468 | Policy gate detail panel — expression, current value, evaluation history | high | m | — |
| 503-ui-bundle-timeline | #466 | Bundle promotion timeline — artifact history with diff links | high | m | — |
| 504-ui-release-metrics | #465 | Release efficiency dashboard — inline metrics on pipeline detail | high | m | — |
| 505-ui-fleet-dashboard | #467 | Global health dashboard — fleet-wide pipeline status | high | l | — |
| 506-ui-actions | #464 | In-UI actions — approve, pause, rollback, override, restart step | critical | xl | 500, 501 |

## Notes

Items 500-505 can be implemented in parallel (all React UI, no new backend needed).
Item 506 (in-UI actions) requires backend API additions for pause/resume/override; depends on 500+501 for the surfaces these actions appear on.

All items are pure UI enhancements — no new CRDs, no new reconcilers, no Graph-purity concerns.
Backend APIs for 506 may need: POST /api/v1/ui/pause, /resume, /override-gate endpoints.
