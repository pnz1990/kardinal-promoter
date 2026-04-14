# Spec: 504-ui-release-metrics

> Feature: Release efficiency dashboard — inline metrics on pipeline detail
> Issue: #465
> Milestone: v0.6.0 — Pipeline Expressiveness
> Graph-purity: N/A (pure UI)

## Problem

There is no visibility into whether a pipeline is getting healthier or sicker over time.

## Acceptance Criteria

**FR-504-01**: Pipeline detail shows a metrics bar with: mean time to production (last 10 bundles), rollback rate (%), avg interventions per deployment.

**FR-504-02**: Each metric has a trend indicator (↑/↓/=) based on previous 10 vs prior 10 bundles.

**FR-504-03**: Metrics are computed client-side from bundle history data (no new API).

**FR-504-04**: Empty state: "Not enough data (need 5+ bundles)" if fewer than 5 bundles.

## Implementation Notes

- Compute from `api.listBundles(pipelineName)` — Bundle list has timestamps, phases, rollback labels
- Mean TTP = average of `(bundle.status.environments['prod'].healthCheckedAt - bundle.metadata.creationTimestamp)` across Verified bundles
- Rollback rate = count of `kardinal/rollback` labeled bundles / total
- Client-side computation, no backend
