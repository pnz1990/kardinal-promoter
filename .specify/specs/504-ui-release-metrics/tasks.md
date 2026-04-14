# Tasks: 504-ui-release-metrics

## Tasks

- [ ] Read `types.ts` for Bundle fields (creationTimestamp, status.environments, labels)
- [ ] Create `ReleaseMetricsBar.tsx` — three stat boxes with trend indicator
- [ ] Compute mean TTP: avg of (prod healthCheckedAt - createdAt) for Verified bundles
- [ ] Compute rollback rate: rollback-labeled bundles / total
- [ ] Compute mean interventions: count overrides per bundle
- [ ] Trend: compare last 10 vs prior 10 bundles
- [ ] Empty state for < 5 bundles
- [ ] Wire into pipeline detail view (above Timeline tab)
- [ ] Write vitest tests for each metric calculation
