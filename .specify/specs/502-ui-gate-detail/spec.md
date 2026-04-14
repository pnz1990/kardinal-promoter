# Spec: 502-ui-gate-detail

> Feature: Policy gate detail panel — expression, current value, evaluation history
> Issue: #468
> Milestone: v0.6.0 — Pipeline Expressiveness
> Graph-purity: N/A (pure UI)

## Problem

When a PolicyGate is blocking, operators cannot see the current evaluated variable values, last evaluation time, or how long it has been blocking.

## Acceptance Criteria

**FR-502-01**: Clicking a gate node opens a detail panel showing: gate name, CEL expression (syntax highlighted), current `status.ready` and `status.reason`.

**FR-502-02**: Show `status.lastEvaluatedAt` with relative time ("evaluated 2m ago").

**FR-502-03**: Show "blocking for X minutes" if `status.ready=false`.

**FR-502-04**: If the gate has override history, show each override: reason, created-by, expires-at.

**FR-502-05**: Panel closes on outside click or Escape.

## Implementation Notes

- Extend existing `PolicyGatesPanel.tsx` or create `GateDetailPanel.tsx`
- Gate data from `api.listGates()` — PolicyGate CRD status already has `lastEvaluatedAt`, `reason`, `ready`
- CEL syntax highlighting: use a lightweight approach (styled spans or a tiny tokenizer)
- No new backend API needed
