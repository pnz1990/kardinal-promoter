# Item: 034-fix-cel-import-ban

> dependency_mode: merged
> depends_on: 010-policygate-cel

## Summary

`cmd/kardinal/cmd/policy.go` directly imports `pkg/cel` to evaluate PolicyGate expressions for `kardinal policy simulate`. This violates the Graph-first architecture ban (AGENTS.md anti-patterns, docs/design/10-graph-first-architecture.md).

## GitHub Issue

#137 — arch(cli): kardinal policy simulate imports pkg/cel — banned outside policygate reconciler

## Acceptance Criteria

- `pkg/cel` is NOT imported in any file outside `pkg/reconciler/policygate/`
- `kardinal policy simulate` still works correctly (moves simulation to server side or uses a different approach)
- Option A: Create a lightweight `simulate` endpoint on the controller that the CLI calls
- Option B: Replicate only the schedule-building logic (no CEL) client-side and call the existing `pkg/reconciler/policygate` simulate function via API
- `go vet ./...` passes, no banned imports remain

## Files to modify

- `cmd/kardinal/cmd/policy.go` (remove pkg/cel import)
- `pkg/api/` or new HTTP endpoint (if option A)
- Tests updated

## Size: M
