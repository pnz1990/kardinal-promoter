# Item 404: K-10 — cross-stage history CEL functions

> Queue: queue-017
> Issue: #453
> Priority: high
> Size: m
> Milestone: v0.6.0 — Pipeline Expressiveness

## Summary

New CEL functions on the `upstream` context that query Bundle promotion history across stages. Reads from `Bundle.status.environments[]` (already populated). No new CRD.

## New CEL functions

- `upstream.<env>.lastNPromotionsSucceeded(n)` — true if the last N successful promotions to <env> all had phase=Verified
- `upstream.<env>.healthContiguousMinutes` — alias for existing soakMinutes (rename for clarity)
- `upstream.<env>.lastPromotedAt` — timestamp (RFC3339 string) of last successful promotion to <env>
- `upstream.<env>.promotionCount` — total number of Verified promotions to <env>

## Acceptance Criteria

- [ ] All 4 functions accessible in CEL gate expressions
- [ ] `lastNPromotionsSucceeded(n)` queries Bundle list for the pipeline filtered by phase=Verified; checks last N entries for <env>
- [ ] `lastPromotedAt` returns latest `status.environments[<env>].healthCheckedAt` from most recent Verified Bundle
- [ ] Returns safe defaults: `lastNPromotionsSucceeded(n)` returns false when fewer than N Bundles exist
- [ ] Unit tests cover all 4 functions with table-driven scenarios (0 bundles, 1 bundle, n+1 bundles)
- [ ] `docs/policy-gates.md` updated with cross-stage history function reference
- [ ] Example gate using `lastNPromotionsSucceeded(3)` in `examples/policy-gates/`

## Package

`pkg/cel/context.go` — extend upstream context with history functions
`pkg/cel/evaluator.go` — register functions with bundle lister injection
`pkg/reconciler/policygate/reconciler.go` — inject bundle lister into evaluator context
