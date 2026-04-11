# Queue 011 — E2E Test Infrastructure + Rendered Manifests

> **Generated**: 2026-04-11
> **Stage**: Cross-cut (E2E verification) + J6 (Rendered manifests)
> **Roadmap ref**: docs/aide/roadmap.md Stage 6 partial (kustomize-build), test infra
> **Batch size**: 2 items (can be parallelized)
> **Status**: active

---

## Purpose

v0.3.0 has 0 open milestone issues but journeys J1-J5 are not E2E verified. This batch:

1. **023** — E2E test infrastructure on kind cluster in CI → verifies J1, J3, J4, J5 simultaneously, enabling v0.3.0 release cut
2. **024** — Rendered manifests (layout: branch + kustomize-build routing) → enables J6 journey

Both items are independent and can be worked in parallel.

Dependencies:
- 023 depends on 013, 014 (all merged ✅)
- 024 depends on 012, 013 (all merged ✅)

---

## Items

| ID | Branch | Title | Depends on | Assignable |
|---|---|---|---|---|
| 023 | 023-e2e-test-infra | E2E test infrastructure — kind cluster CI | 013, 014 (merged) | immediately |
| 024 | 024-rendered-manifests | Rendered manifests — layout:branch + kustomize-build | 012, 013 (merged) | immediately |

---

## Journey impact

- J1 (Quickstart): 023 provides the E2E verification that marks J1 ✅
- J3 (Policy governance): 023 includes policy simulate test → marks J3 ✅
- J4 (Rollback): 023 verifies rollback trigger → marks J4 ✅ (auto-rollback in CI)
- J5 (CLI workflow): 023 validates CLI output format
- J6 (Rendered manifests): 024 implements the branch-layout step sequence
