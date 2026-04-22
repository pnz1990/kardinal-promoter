# Spec: issue-1047 — maxConcurrentPromotions cap per pipeline

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future`
- **Implements**: No `maxConcurrentPromotions` cap per pipeline (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1** — `Pipeline.spec.maxConcurrentPromotions` is a new optional int field.
When 0 or unset: unlimited concurrent promotions (backward-compatible).
When N > 0: at most N Bundles for this pipeline may be in Promoting phase simultaneously.

**O2** — The enforcement is in `handleAvailable` in `pkg/reconciler/bundle/reconciler.go`,
before calling `r.Translator.Translate`. If the cap is reached, the Bundle is requeued
with a short delay (30s) and NOT advanced to Promoting. No error condition is set;
`Result{RequeueAfter: 30s}` is returned.

**O3** — The cap check counts Bundles in this namespace with `spec.pipeline == pipeline.Name`
AND `status.phase == "Promoting"`. Available + Superseded + Verified + Failed bundles
do not count toward the cap.

**O4** — A unit test covers the enforcement: given a pipeline with maxConcurrentPromotions=1
and one Promoting bundle, a second Available bundle must be requeued (not advanced to Promoting).
When the first bundle is Verified, the second bundle advances.

**O5** — The field is validated: `+kubebuilder:validation:Minimum=0`; default is 0 (unlimited).

---

## Zone 2 — Implementer's judgment

- Where to add cap check: after successful Pipeline get, before `r.Translator == nil` check
  (fail-safe: nil translator still passes, cap is enforced independently).
- Cap check implementation: list Bundles in namespace, count phase="Promoting" matching
  pipeline name. Use `client.MatchingFields` for efficiency if the index exists, or fallback
  to list+filter.
- RequeueAfter: 30s is short enough to not miss a Promoting→Verified transition.
- Log message on cap hit: `log.Info().Int("active", activeCount).Int("cap", cap).Msg("maxConcurrentPromotions cap reached — requeuing")`

---

## Zone 3 — Scoped out

- Per-environment concurrency (only per-pipeline)
- Prometheus metric for cap-hit events (follow-up)
- Status condition showing "waiting for promotion slot" (follow-up)
