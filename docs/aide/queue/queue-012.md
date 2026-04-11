# Queue 012 — Pause/Resume + Kind Cluster E2E

> **Generated**: 2026-04-11
> **Stage**: Stage 13 completion + real-cluster E2E verification
> **Roadmap ref**: docs/aide/roadmap.md Stage 13 (Rollback/Pause/Resume), E2E journeys
> **Batch size**: 2 items (can be parallelized)
> **Status**: active

---

## Purpose

Stage 13 (Rollback/Pause/Resume) is partially complete: auto-rollback (item 022) and
manual rollback CLI (item 015) are done. The remaining gap is the pause/resume
reconciler — the freeze-gate injection that `kardinal pause` injects needs to be
reconciler-aware (the policygate reconciler must honor the freeze gate, and `kardinal resume`
must remove it idempotently).

Additionally, all journeys J1/J3/J4/J5 have fake-client tests passing but no real kind cluster
verification. A GitHub Actions workflow with a kind cluster would mark these journeys ✅ and
allow v0.2.0 release to be cut.

Items:
1. **025** — Pause/resume reconciler: freeze-gate injection + policygate honors freeze + idempotent resume
2. **026** — Kind cluster E2E GitHub Actions workflow: runs J1/J3/J4 on real kind cluster in CI

Both items are independent and can be worked in parallel.

Dependencies:
- 025 depends on 010, 015 (PolicyGate reconciler + full CLI — all merged ✅)
- 026 depends on 023 (fake-client E2E — merged ✅)

---

## Items

| ID | Branch | Title | Depends on | Assignable |
|---|---|---|---|---|
| 025 | 025-pause-resume | Pause/resume: freeze-gate injection + policygate honors freeze | 010, 015 (merged) | immediately |
| 026 | 026-kind-e2e | Kind cluster E2E GitHub Actions workflow for J1/J3/J4 | 023 (merged) | immediately |

---

## Journey impact

- J4 (Rollback): 025 completes pause/resume, enabling full J4 rollback flow (pause, rollback PR, resume)
- J1 (Quickstart): 026 verifies the full promotion on a real cluster → marks J1 ✅
- J3 (Policy governance): 026 runs no-weekend-deploys gate on a real cluster → marks J3 ✅
- J4 (Rollback): 026 verifies rollback PR on a real cluster → marks J4 ✅
