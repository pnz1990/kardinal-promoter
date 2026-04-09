# 04: PolicyGate Reconciler

> Status: Outline
> Depends on: 01-graph-integration
> Blocks: nothing (leaf node)

Evaluates CEL policy expressions and manages the timer-based recheck loop.

## Scope

- CEL context specification: every attribute, its Go type, how it's computed, which phase it's available in
  - Phase 1: bundle.version, bundle.labels.*, bundle.provenance.*, bundle.intent.*, schedule.isWeekend, schedule.hour, schedule.dayOfWeek, environment.name, environment.approval
  - Phase 2: metrics.*, bundle.upstreamSoakMinutes, previousBundle.version
  - Phase 3: delegation.status, externalApproval.*
  - Phase 4+: contracts.*, targetDrift.unreconciled
- CEL environment setup: how the cel-go environment is configured, which functions/macros are available
- Context building: how the reconciler assembles the context struct from CRD data (Bundle, PromotionStep upstream status, schedule from system clock)
- Timer-based recheck: goroutine model, how the timer pool works, what happens at scale (100 gates x 5m recheck = 20 writes/min)
- lastEvaluatedAt: how it's written, how Graph's readyWhen checks freshness, what staleness window is acceptable
- Skip-permission evaluation: runs synchronously in the Pipeline-to-Graph translator (02), not in this reconciler. But the CEL context building is shared. Document the boundary.
- Error handling: CEL parse errors (fail-closed), unknown attributes from future phases (fail-closed with reason message), evaluation timeouts
- Template vs instance: PolicyGate CRDs in platform-policies are templates. Per-Bundle instances are created by Graph. The reconciler only evaluates instances, not templates.
