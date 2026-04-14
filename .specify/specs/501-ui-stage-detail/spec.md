# Spec: 501-ui-stage-detail

> Feature: Per-stage approval workflow detail — step states, bake countdown
> Issue: #463
> Milestone: v0.6.0 — Pipeline Expressiveness
> Graph-purity: N/A (pure UI)

## Problem

When a stage is in progress or failed, the operator cannot see which specific step is running, bake progress, or integration test pass rates.

## Acceptance Criteria

**FR-501-01**: Clicking a stage node in the DAG view opens a detail panel showing all PromotionSteps for that stage with their current phase.

**FR-501-02**: If a `bake:` duration is configured, show a countdown timer (e.g. "Baking: 12m / 30m").

**FR-501-03**: If an integration test step exists, show pass/fail count across recent bundles.

**FR-501-04**: Show the PR URL if the stage has an open PR (from PRStatus CRD data via api.getSteps).

**FR-501-05**: Panel closes on outside click or pressing Escape.

## Implementation Notes

- Extend `NodeDetail.tsx` or create `StageDetailPanel.tsx`
- Data from `api.getSteps(bundleName)` — PromotionStep list already includes step type and phase
- Bake countdown: compute from `step.status.startedAt` + `pipeline.spec.environments[].bake`
- No new backend API needed
