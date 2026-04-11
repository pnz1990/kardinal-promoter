# Queue 010 — Stage 12 (Helm + config-only promotions) + Stage 13 partial (auto-rollback)

> **Generated**: 2026-04-11
> **Stage**: Stage 12 (Helm strategy + config promotions), Stage 13 partial (auto-rollback)
> **Roadmap ref**: docs/aide/roadmap.md Stages 12, 13
> **Batch size**: 2 items (parallel)
> **Status**: active

---

## Purpose

J1 is now code-complete (Stages 0-11 merged). With E2E testing blocked by environment,
this queue expands the product into Phase 2 capabilities:

1. **021** — Helm update strategy + config-only Bundle type → enables Helm users and config promotions
2. **022** — Automatic rollback on health failure → completes J4 automation

Both items are independent and can be worked in parallel.

Dependencies:
- 021 depends on 013 (PromotionStep reconciler, done ✅)
- 022 depends on 013 (PromotionStep reconciler, done ✅) and 014 (health adapters, done ✅)

---

## Items

| ID | Branch | Title | Depends on | Assignable |
|---|---|---|---|---|
| 021 | 021-helm-config-promotions | Helm strategy + config-only promotions | 013 (merged) | immediately |
| 022 | 022-auto-rollback | Automatic rollback on health failure | 013, 014 (merged) | immediately |

---

## Journey impact

- J4 (Rollback): 022 adds the automatic rollback leg — fully completes J4 code
- J1 (Quickstart): 021 expands what pipelines can promote (Helm + config Bundles)
