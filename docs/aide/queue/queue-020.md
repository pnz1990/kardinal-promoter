# Queue 020 — Bug Fix + Architecture Debt

> Created: 2026-04-14
> Status: Active
> Purpose: Fix critical CLI bug + begin pkg/cel elimination

## Items

| Item | Issue | Title | Priority | Size | Depends on |
|---|---|---|---|---|---|
| 700-policy-simulate-fix | #483 | bug(cli): policy simulate namespace fix | high | xs | — |
| 701-eliminate-pkg-cel | #130 | arch: eliminate pkg/cel standalone evaluator | critical | l | 700 (independent) |

## Notes

Item 700 fixes the policy simulate CLI bug that blocks Journey 3 validation.

Item 701 eliminates pkg/cel — now feasible with ScheduleClock providing watch-driven
re-evaluation. Per docs/design/11-graph-purity-tech-debt.md:
- PolicyGate reconciler reads schedule.* from CRD status context (already works)
- CLI policy simulate calls server-side API (item 700 fix contributes to this path)
- No krocodile changes needed
