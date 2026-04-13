# Queue 017 — v0.6.0 Pipeline Expressiveness: K-06 through K-10

> Created: 2026-04-13
> Status: Active
> Purpose: v0.6.0 — Pipeline Expressiveness milestone — five features that deepen the DAG moat

## Items

| Item | Issue | Title | Priority | Size | Depends on |
|---|---|---|---|---|---|
| 400-wave-topology | #450 | K-06: wave: field on stages as DAG shorthand | high | m | 009 |
| 401-integration-test-step | #449 | K-07: integration-test step (Kubernetes Job) | high | m | 013 |
| 402-pr-review-gate | #452 | K-08: bundle.pr().isApproved() CEL function | high | s | 036 |
| 403-kardinal-override | #451 | K-09: kardinal override with audit record | high | s | 011 |
| 404-cross-stage-history-cel | #453 | K-10: cross-stage history CEL functions | high | m | 036 |

## Notes

Items 400 and 401 can be implemented in parallel (different packages).
Items 402 and 403 can be implemented in parallel (different packages).
Item 404 depends on Bundle.status.environments structure (done) and PRStatus CRD (done).

All five are graph-first compliant — reviewed in SPEC GATE CLEAR comment on Issue #1.
