# Item 505: Global Health Dashboard — fleet-wide pipeline status

> Queue: queue-018
> Issue: #467
> Priority: high
> Size: l
> Milestone: v0.6.0 — UI Control Plane

## Summary

Replace the current home page (`/`) with a two-zone layout: fleet health summary bar at top
(counts of healthy/blocked/needs-human/CI-red pipelines) + pipeline table below (same as
issue #462 ops view). Add a recent activity feed. Requires a new `/api/v1/events` API endpoint.

## Acceptance Criteria

- [ ] Home page shows fleet health summary bar: Pipelines / Healthy / Blocked / Needs Human / CI Red / Full CD counts
- [ ] Each count in the summary bar is a link that filters the pipeline list
- [ ] Pipeline table is the same sortable table from issue #462 (already merged as PR #475)
- [ ] Default sort: blocked pipelines first
- [ ] Recent activity feed shows last 10 events (promotion_complete, auto_rollback, gate_override, bundle_created)
- [ ] New backend API: `GET /api/v1/events?limit=50` returning array of recent promotion events
  sourced from Kubernetes Events emitted by PromotionStep reconciler
- [ ] Frontend tests cover: summary bar counts, filter behavior, activity feed rendering
- [ ] Backend tests cover: GET /api/v1/events with Kubernetes Event listing

## Package

`web/src/` — HomePage refactored with FleetSummaryBar + ActivityFeed components
`web/src/api/` — new events() API call
`internal/api/` or `cmd/kardinal-controller/` — GET /api/v1/events handler
