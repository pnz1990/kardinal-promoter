# Item 030: Fix kardinal explain Zero PolicyGates (Label Mismatch)

> **Stage**: Workshop 1 Parity (CLI fix)
> **Queue**: queue-014
> **Priority**: critical
> **Size**: s
> **Depends on**: 013 (PromotionStep reconciler)
> **dependency_mode**: merged
> **GitHub issue**: #116

## Context

`kardinal explain nginx-demo --env prod` shows zero PolicyGates even when gates exist.

Root cause: label key mismatch between builder and explain command:
- Graph builder sets `"kardinal.io/gate-template": gate.Name` on PolicyGate nodes
- explain command queries with a different label key

## Acceptance Criteria

- `kardinal explain <pipeline> --env <env>` shows all active PolicyGate nodes
- Each gate shows: NAME / STATE / REASON
- Label key used for querying PolicyGate nodes must match what the Graph builder sets
- No regression on existing explain output fields (environment, step names, etc.)

## Files to Modify

- `cmd/kardinal/cmd/explain.go` — fix label key used for PolicyGate lookup
- Possibly `pkg/graph/builder.go` — verify/standardize the label key used

## Tasks

- [ ] T001 Read explain.go and builder.go to find the exact mismatch
- [ ] T002 Write failing test reproducing the zero-gate scenario
- [ ] T003 Fix the label key mismatch
- [ ] T004 Verify go test -race passes
