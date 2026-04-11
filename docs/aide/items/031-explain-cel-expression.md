# Item 031: Show CEL Expression + Current Value in kardinal explain

> **Stage**: Workshop 1 Parity (CLI enhancement)
> **Queue**: queue-014
> **Priority**: critical
> **Size**: s
> **Depends on**: 010 (PolicyGate CEL evaluator), 013 (PromotionStep reconciler)
> **dependency_mode**: merged
> **GitHub issue**: #117

## Context

`kardinal explain` output currently shows: ENVIRONMENT / TYPE / NAME / STATE / REASON

The definition-of-done.md J3 requires: _"shows CEL expression, current value, and result"_

The CEL expression (`spec.expression`) and the current evaluated value are never displayed.

## Acceptance Criteria

- `kardinal explain <pipeline> --env <env>` output for each PolicyGate node:
  - EXPRESSION column (or field): shows `spec.expression` (e.g. `!schedule.isWeekend`)
  - VALUE column (or field): shows the current evaluated value (e.g. `isWeekend=false` or `soakMinutes=45`)
  - RESULT column: PASS / BLOCK / PENDING
- Implementation reads from `PolicyGate.status` fields (lastEvaluatedAt, ready, message)
- Expression text read from `PolicyGate.spec.expression` directly
- When gate hasn't been evaluated yet: VALUE shows `-`, RESULT shows `Pending`

## Files to Modify

- `cmd/kardinal/cmd/explain.go` — extend output table for PolicyGate rows

## Tasks

- [ ] T001 Read explain.go to understand current output structure
- [ ] T002 Write failing test for CEL expression column
- [ ] T003 Implement expression + current value display
- [ ] T004 Verify go test -race passes
