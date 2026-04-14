# Spec: 503-ui-bundle-timeline

> Feature: Bundle promotion timeline — artifact history with diff links and audit trail
> Issue: #466
> Milestone: v0.6.0 — Pipeline Expressiveness
> Graph-purity: N/A (pure UI)

## Problem

There is no way to see the history of what has been promoted through a pipeline, when, by whom, and what changed.

## Acceptance Criteria

**FR-503-01**: Pipeline detail shows a `Timeline` tab with one row per Bundle (sorted newest first): version, phase, promoted-at, promoted-by (from provenance), per-env status chips.

**FR-503-02**: Each row links to the PR for the production environment (if available from Bundle.status.environments).

**FR-503-03**: Superseded/Failed bundles are shown with dimmed styling.

**FR-503-04**: First 20 bundles shown; "Load more" expands to all.

**FR-503-05**: If a bundle has override records, show an audit indicator.

## Implementation Notes

- Extend or replace `BundleTimeline.tsx` (already exists as a partial)
- Data from `api.listBundles(pipelineName)` — Bundle list has provenance and environment statuses
- PR links from `bundle.status.environments[].prURL` field
- No new backend API needed
