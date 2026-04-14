# Item 504: Release Efficiency Dashboard — inline metrics on pipeline detail

> Queue: queue-018
> Issue: #465
> Priority: high
> Size: m
> Milestone: v0.6.0 — UI Control Plane

## Summary

Add an inline metrics bar at the top of the pipeline detail page showing key delivery health
indicators sourced from `Pipeline.status.deploymentMetrics` (K-05, #447). Renders with `--`
placeholder values when the backend field is absent (K-05 not yet deployed).

## Acceptance Criteria

- [ ] Metrics bar appears at top of PipelineDetail page with 5 columns:
      Inventory Age, Last Merge, Interventions/deploy, Blockage Time, P90 to Prod
- [ ] Each metric shows a color-coded status chip: green / amber / red per thresholds in issue #465
- [ ] Values are `--` when `Pipeline.status.deploymentMetrics` is absent (graceful degradation)
- [ ] Sparkline chart (last 30 deployments) for P50/P90 commit-to-production trend
- [ ] Frontend tests cover: all 5 metrics rendering, threshold color logic, missing data case

## Package

`web/src/components/` — new MetricsBar + TrendSparkline components
`web/src/` — PipelineDetail page extended with MetricsBar

## Notes

K-05 backend is issue #447 (deployment metrics). Implement the UI component now with
placeholder support. Wire up real data when K-05 merges.
