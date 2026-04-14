# Spec: 500-ui-pipeline-ops-view

> Feature: Pipeline list operations view — sortable health columns
> Issue: #462
> Milestone: v0.6.0 — Pipeline Expressiveness
> Graph-purity: N/A (pure UI — reads existing API data)

## Problem

The current pipeline list shows name, phase, and a paused badge. Operators cannot see at a glance which pipelines need attention.

## Acceptance Criteria

**FR-500-01**: Pipeline list shows sortable columns: Pipeline, Bundle (current), Age (staleness), In Progress, Blocked By, Interventions/Deploy.

**FR-500-02**: Each row has a colored health indicator: green (all passing), yellow (warning), red (blocked/failed).

**FR-500-03**: Blocked pipelines float to the top by default.

**FR-500-04**: Clicking a row navigates to the pipeline detail.

**FR-500-05**: Column sort is preserved across poll cycles (no table jump on refresh).

## Implementation Notes

- Modify `PipelineList.tsx` — augment columns from existing `api.listPipelines()` data
- Staleness = `bundle.status.updatedAt` age; Blocked By = count of gates with `status.ready=false`
- No new backend API needed — all fields exist in Pipeline/Bundle CRDs already
- Use existing `HealthChip` component for status column
