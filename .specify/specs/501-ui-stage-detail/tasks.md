# Tasks: 501-ui-stage-detail

## Tasks

- [ ] Read existing `NodeDetail.tsx`, `DAGView.tsx`, `types.ts`
- [ ] Create `StageDetailPanel.tsx` — step list with phases and timestamps
- [ ] Add bake countdown timer from startedAt + pipeline bake duration
- [ ] Show PR URL from step outputs (open-pr step output field)
- [ ] Show integration test pass rate from recent bundle history (if available)
- [ ] Panel open/close on node click; close on outside click / Escape
- [ ] Write vitest tests for bake countdown calculation
- [ ] Wire panel into `DAGView.tsx` node click handler
