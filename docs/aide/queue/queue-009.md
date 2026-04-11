# Queue 009 — Stage 9 (Embedded React UI) + Stage 11 (Bundle Webhook + GitHub Action)

> **Generated**: 2026-04-11
> **Stage**: Stage 9 (Embedded React UI), Stage 11 (Bundle Webhook + GitHub Action)
> **Roadmap ref**: docs/aide/roadmap.md Stages 9, 11
> **Batch size**: 2 items (parallel)
> **Status**: active

---

## Purpose

With Stages 0-8, 10 complete, the remaining gap for J1 end-to-end is:
1. **Stage 9**: Embedded React UI — visual confirmation of promotion DAG
2. **Stage 11** (partial): Bundle webhook endpoint + GitHub Action — CI can trigger promotions

These items are independent and can be worked in parallel.

Dependencies satisfied:
- 019 (React UI) depends on 013 (PromotionStep reconciler, done ✅) and 014 (health adapters, done ✅)
- 020 (Bundle webhook) depends on 017 (kardinal init, done ✅) and 012 (steps engine, done ✅)

---

## Items

| ID | Branch | Title | Depends on | Assignable |
|---|---|---|---|---|
| 019 | 019-embedded-react-ui | Embedded React UI (Stage 9) | 013, 014 (merged) | immediately |
| 020 | 020-bundle-webhook | Bundle Webhook + GitHub Action (Stage 11 partial) | 012, 017 (merged) | immediately |

---

## Journey impact

- J1 (Quickstart): 019 + 020 bring J1 to full code completeness. After these merge, J1 E2E test should be attempted.
- J5 (CLI): 019 adds UI visibility (bonus).
- J1 completion also enables v0.1.0 release cut.
