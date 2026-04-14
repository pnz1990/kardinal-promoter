# Item 601: ScheduleClock CRD — eliminate pkg/cel timer loop

> Queue: queue-019
> Issue: #138
> Priority: high
> Size: l
> Milestone: v0.7.0 — Graph Purity

## Summary

Add a `ScheduleClock` CRD and reconciler that writes a `status.tick` timestamp on a
configurable interval. PolicyGate reconciler watches ScheduleClock objects and triggers
re-evaluation when `status.tick` changes. This eliminates the `pkg/cel` timer loop
workaround documented in `docs/design/10-graph-first-architecture.md §Known Exceptions`.

## Acceptance Criteria

- [ ] `ScheduleClock` CRD with spec.intervalSeconds, status.tick, status.lastTickAt
- [ ] ScheduleClockReconciler: writes status.tick on interval, idempotent
- [ ] PolicyGate reconciler: `SetupWithManager` adds a Watch on ScheduleClock objects
  that enqueues all PolicyGates in the same namespace on tick
- [ ] `pkg/reconciler/policygate/reconciler.go` no longer needs `ctrl.Result{RequeueAfter}`
  for schedule-based gates (the clock drives re-evaluation instead)
- [ ] ScheduleClock auto-created per namespace by controller on startup if not present
- [ ] Unit tests: tick interval, idempotency, multiple gates receiving tick events
- [ ] `docs/policy-gates.md` updated with ScheduleClock architecture note

## Package

`api/v1alpha1/scheduleclock_types.go` — new CRD types
`pkg/reconciler/scheduleclock/reconciler.go` — new reconciler
`pkg/reconciler/policygate/reconciler.go` — add ScheduleClock watch in SetupWithManager
`cmd/kardinal-controller/main.go` — register ScheduleClockReconciler
