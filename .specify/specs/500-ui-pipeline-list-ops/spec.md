# 500-ui-pipeline-list-ops — Pipeline Operations View

## Summary
Add a sortable/filterable operations table view for the pipeline list page. Closes #462.

## Problem
Current sidebar shows name + phase only. Operators cannot triage at a glance which
pipelines need attention — stuck promotions, blocker gates, failed steps, stale inventory.

## What to build

### Backend (ui_api.go)
Extend `uiPipelineResponse` with:
- `blockerCount` int — count of pre-deploy PolicyGates with ready=false for active bundle
- `failedStepCount` int — count of PromotionSteps with state=Failed for active bundle
- `inventoryAgeDays` int — days since latest bundle was created (stale inventory signal)
- `lastMergedAt` string — RFC3339 timestamp of last successful prod promotion (last env Verified)
- `cdLevel` string — "full-cd" / "mostly-cd" / "no-cd" derived from PolicyGate count per env

### Frontend (PipelineOpsTable.tsx)
New component rendering a full-width table with:
- Sortable columns: Name, Status, Active Bundle, Blockers, Failed Steps, Inventory Age, Last Merge
- Default sort: Blocked first, then by Blockers descending
- Filter bar: filter by name substring or status
- Color coding: red for blocked/failed, amber for stale inventory (>14 days)
- Clickable rows → select pipeline

### App.tsx
- Toggle button: "List" (current sidebar) vs "Ops Table" (new table view)
- When table is active, main content area shows only the table (no DAG)

## Architecture
Pure frontend + API response extension.
No new CRDs. No CEL. No reconciler changes.
All data derived from existing Bundle + PromotionStep + PolicyGate CRD fields.
