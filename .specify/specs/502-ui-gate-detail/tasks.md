# Tasks: 502-ui-gate-detail

## Tasks

- [ ] Read existing `PolicyGatesPanel.tsx`, `NodeDetail.tsx`, `types.ts`
- [ ] Create `GateDetailPanel.tsx` with: expression (highlighted), status, lastEvaluatedAt, blocking duration
- [ ] Add CEL syntax highlighting (token-level spans: keywords, strings, identifiers)
- [ ] Show override history from `spec.overrides[]` with reason, expires-at, created-by
- [ ] Wire panel into DAGView gate node click handler
- [ ] Close on outside click / Escape
- [ ] Write vitest tests for "blocking for X minutes" calculation
