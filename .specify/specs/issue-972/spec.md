# Spec: Add Prometheus metrics for step duration and gate blocking time

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future` (Lens 3 — Observability)
- **Implements**: Missing Prometheus metrics for step duration and gate blocking time (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

O1. `pkg/reconciler/observability/metrics.go` must export a `StepDurationSeconds`
    histogram with label `step` (e.g. "git-clone", "kustomize", "open-pr").
    Violation: metric absent or label is not "step".

O2. `pkg/reconciler/observability/metrics.go` must export a `GateBlockingDurationSeconds`
    histogram tracking how long a PolicyGate has been blocking.
    Violation: metric absent.

O3. `pkg/reconciler/observability/metrics.go` must export a `PromotionStepAgeSeconds`
    histogram recording in-flight PromotionStep age when a step enters a terminal state.
    Violation: metric absent.

O4. All three new histograms must be registered via `ctrlmetrics.Registry.MustRegister`
    in the `init()` function (same pattern as existing metrics).
    Violation: metric registered via `prometheus.DefaultRegisterer` or not in `init()`.

O5. The policygate reconciler must call `GateBlockingDurationSeconds.Observe()` when a
    gate transitions from blocked to allowed (non-Ready → Ready).
    Violation: no call to `GateBlockingDurationSeconds.Observe` in policygate reconciler.

O6. The promotionstep reconciler must call `StepDurationSeconds.Observe()` when a
    built-in step completes (succeeds or fails).
    Violation: no call to `StepDurationSeconds.Observe` in promotionstep reconciler.

O7. The design doc `docs/design/15-production-readiness.md` must move the
    step-duration/gate-blocking item from 🔲 to ✅.
    Violation: item still 🔲.

O8. Build passes. Violation: any build error.

O9. All tests pass (`go test ./... -race -count=1`). Violation: any failure.

O10. Tests exist for the new metrics (at least register+increment for each).
     Violation: no tests for new metrics.

---

## Zone 2 — Implementer's judgment

- PromotionStepAgeSeconds: whether to observe at Verified or Failed terminal states,
  or both.
- Histogram bucket ranges (step duration: seconds; gate blocking: minutes-to-hours).
- Whether to add a `pipeline` label to histograms (useful for dashboard but increases
  cardinality — prefer no pipeline label for now, keep cardinality low).

---

## Zone 3 — Scoped out

- Reconciler queue depth (requires controller-runtime internals not exposed as public API).
- Per-pipeline cardinality dimensions on histograms.
- Grafana dashboard JSON (tracked separately as design doc 15 Future item).
