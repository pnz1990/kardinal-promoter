# Tasks: 505-ui-fleet-dashboard

## Tasks

- [ ] Read `App.tsx`, `PipelineList.tsx`, `usePolling.ts`, `useRefreshIndicator.ts`
- [ ] Create `FleetDashboard.tsx` — summary bar + needs-attention section
- [ ] Summary bar: total, blocked, verified, paused counts
- [ ] Needs-attention section: blocked/failed pipelines sorted by stuck duration
- [ ] Overall health badge: ON_TRACK / AT_RISK / DEGRADED
- [ ] Collapsible sections (sessionStorage persistence)
- [ ] Wire into App.tsx home route
- [ ] Refresh staleness indicator using `useRefreshIndicator`
- [ ] Write vitest tests for health badge derivation logic
