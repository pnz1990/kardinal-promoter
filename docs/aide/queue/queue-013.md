# Queue 013 — MetricCheck CRD + Custom Promotion Steps

> **Generated**: 2026-04-11
> **Stage**: Stage 15 (MetricCheck + soak time) + Stage 16 (Custom steps via webhook)
> **Roadmap ref**: docs/aide/roadmap.md Stage 15 and Stage 16
> **Batch size**: 2 items (can be parallelized)
> **Status**: active

---

## Purpose

Stage 15 (MetricCheck CRD and upstream soak time) and Stage 16 (Custom promotion steps
via webhook) are both independently deliverable and depend only on already-merged stages.

Stage 15 advances Journey 3 (Policy governance) by enabling metric-based gates that
use real Prometheus data. Upstream soak time in CEL context enables the
`staging-soak-30m` gate pattern described in definition-of-done.md J3.

Stage 16 enables teams to inject custom logic into the promotion sequence via HTTP
webhooks. This is a prerequisite for many real-world use cases.

Both items are independent and can be worked in parallel.

Dependencies:
- 027 depends on 010 (PolicyGate CEL evaluator — merged ✅)
- 028 depends on 013 (PromotionStep reconciler — merged ✅)

---

## Items

| ID | Branch | Title | Depends on | Assignable |
|---|---|---|---|---|
| 027 | 027-metriccheck-crd | MetricCheck CRD, Prometheus evaluator, soak time in CEL | 010 (merged) | immediately |
| 028 | 028-custom-steps | Custom promotion steps via HTTP webhook dispatch | 013 (merged) | immediately |

---

## Journey impact

- J3 (Policy governance): 027 enables `staging-soak-30m` gate (upstream.uat.soakMinutes) and metric gates (metrics.error_rate.value)
- J3 (Policy governance): `kardinal policy simulate` now shows metric values in output
- Stage 16: custom steps enable external test steps in promotion sequences
