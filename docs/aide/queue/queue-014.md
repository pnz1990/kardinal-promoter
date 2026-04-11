# Queue 014 — Workshop 1 Parity Fixes

> Created: 2026-04-11
> Status: Active
> Purpose: Fix the three CLI issues blocking Workshop 1 execution

## Items

| Item | Title | Priority | Size | Depends on |
|---|---|---|---|---|
| 029-pipeline-table-columns | Fix FormatPipelineTable per-environment columns (#115) | critical | s | 011, 013 |
| 030-explain-policygate-labels | Fix kardinal explain zero PolicyGates label mismatch (#116) | critical | s | 013 |
| 031-explain-cel-expression | Show CEL expression + current value in kardinal explain (#117) | critical | s | 010, 013 |

## Notes

These three fixes unblock Workshop 1 execution (epic #123).
All three can be implemented in parallel since they touch different files.
After merge, the standalone agent should execute Workshop 1 on a live kind cluster.
