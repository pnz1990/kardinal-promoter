# Queue 004 — Pre-Stage 3 Fix + Stage 3: Graph Generation and kro Integration

> **Generated**: 2026-04-10
> **Stage**: Pre-Stage-3 fix (item 008) + Stage 3 — Graph Generation and kro Integration
> **Roadmap ref**: docs/aide/roadmap.md Stage 3
> **Batch size**: 2 items (008 blocking fix first, then 009 Stage 3)
> **Status**: active

---

## Purpose

Item 008 is a blocking pre-queue fix that must merge before any Stage 3 items.

Two bugs were discovered during the PM spec gate for Stage 3:
1. `PropagateWhen` missing from `GraphNode` (PolicyGates would silently bypass)
2. kro Graph API group changed from `kro.run` to `experimental.kro.run` (krocodile commit `48224264`)

Stage 3 items cannot be assigned until item 008 is merged.

---

## Items

| ID | Branch | Title | Depends on | Assignable |
|---|---|---|---|---|
| 008 | 008-graph-types-propagate-when | Add PropagateWhen + fix Graph API group | 007 (merged) | immediately |
| 009 | 009-graph-builder | Graph Builder + BundleReconciler Graph creation | 008 (merged) | after 008 merges |

---

## Assignment Wave 1

- **008-graph-types-propagate-when** → STANDALONE-ENG (blocking fix, assign immediately)

## Assignment Wave 2 (after 008 merges)

- **009-graph-builder** → STANDALONE-ENG (depends on 008 merged)

---

## Acceptance Gate

Both items `done` before advancing to Stage 4 queue.

- `go build ./...` passes
- `go test ./pkg/graph/... -race` passes with PropagateWhen roundtrip test
- Graph Builder creates correct Graph specs for linear and fan-out pipelines
- Unit tests for graph.Builder achieve 90% line coverage
