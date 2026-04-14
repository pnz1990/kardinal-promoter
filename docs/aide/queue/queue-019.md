# Queue 019 — Graph Purity: ScheduleClock + CEL Migration

> Created: 2026-04-14
> Status: Active
> Purpose: Eliminate pkg/cel timer loop and begin graph purity improvements

## Context

All v0.6.0 UI items are complete (queue-018). The next high-value work is graph purity:
1. **ScheduleClock CRD** (#138) — eliminates the `pkg/cel` standalone evaluator timer workaround
2. **Subscription CRD** (Stage 18) — CI-less onboarding; creates Bundles from OCI/Git watching

The critical architecture items (#130, #132) are tracked but the ScheduleClock is the right
first step because it directly eliminates the documented transitional workaround in
`docs/design/10-graph-first-architecture.md`.

## Items

| Item | Issue | Title | Priority | Size | Depends on |
|---|---|---|---|---|---|
| 601-scheduleclock-crd | #138 | ScheduleClock CRD + schedule CEL library — eliminate pkg/cel timer loop | high | l | — |
| 602-subscription-crd | Stage 18 | Subscription CRD — OCI + Git watching for CI-less bundle creation | medium | xl | 601 (independent) |

## Notes

Item 601 is the most impactful graph purity fix that doesn't require krocodile changes.
Per `docs/design/11-graph-purity-tech-debt.md §ScheduleClock Implementation`:
- `ScheduleClock` CRD + reconciler (writes status.tick on interval)
- `schedule.*` CEL library registered on Graph DefaultEnvironment  
- Graph builder auto-wires clock reference to PolicyGate nodes with schedule.* expressions
- Deletes the PolicyGate reconciler's ctrl.Result{RequeueAfter} timer loop

Item 602 (Subscription CRD) is Stage 18 from roadmap.md — independent of ScheduleClock.
