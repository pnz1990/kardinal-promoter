# Spec: 505-ui-fleet-dashboard

> Feature: Global health dashboard — fleet-wide pipeline status at a glance
> Issue: #467
> Milestone: v0.6.0 — Pipeline Expressiveness
> Graph-purity: N/A (pure UI)

## Problem

The home page shows a list. There is no fleet-wide health view answering: how many pipelines are blocked, how many have human interventions pending, what is the overall delivery health.

## Acceptance Criteria

**FR-505-01**: Home page (`/`) shows a summary bar at top: total pipelines, blocked count, verified count, paused count.

**FR-505-02**: A "Needs Attention" section shows pipelines in Failed or blocked state, sorted by duration stuck.

**FR-505-03**: An overall health badge: `ON_TRACK` (green), `AT_RISK` (yellow, any blocked), `DEGRADED` (red, any Failed).

**FR-505-04**: Section headers collapse/expand (preserved in sessionStorage).

**FR-505-05**: Refresh staleness indicator shows "Updated X ago" (reuse existing `useRefreshIndicator` hook).

## Implementation Notes

- Replace or augment current home page in `App.tsx`
- Data from `api.listPipelines()` + `api.listGates()`  
- No new backend API
- Health badge derivation: Degraded if any pipeline has env.phase=Failed; At Risk if blocked>0; On Track otherwise
