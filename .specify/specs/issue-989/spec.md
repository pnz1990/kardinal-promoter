<!--
Copyright 2026 The kardinal-promoter Authors.
Licensed under the Apache License, Version 2.0
-->

# Spec: Regression Test — RequeueAfter >= 500ms in Bundle Reconciler

**Item**: issue-989
**Design reference**:
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `Lens 7 — hot loop fix`
- **Implements**: test guard asserting `reconcileAvailable` result.RequeueAfter >= 500ms (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1**: The test `TestBundleReconciler_SetsAvailablePhase` (or a dedicated new test
`TestBundleReconciler_HandleNew_RequeueAfterFloor`) MUST assert
`result.RequeueAfter >= 500*time.Millisecond`.

A violation would be: `assert.Greater(t, result.RequeueAfter, time.Duration(0))`
without the 500ms floor check — this passes even if `RequeueAfter` is 1ms.

**O2**: The test must use `assert.GreaterOrEqual(t, result.RequeueAfter, 500*time.Millisecond)`
(or equivalent) so that any future change setting `RequeueAfter` back to
`time.Millisecond` causes an immediate test failure.

**O3**: The test must compile and pass with `go test ./pkg/reconciler/bundle/... -race`.

**O4**: No new files other than the test change to `pkg/reconciler/bundle/reconciler_test.go`.
This is a pure test addition — no production code change.

---

## Zone 2 — Implementer's judgment

- Whether to strengthen the existing assertion in `TestBundleReconciler_SetsAvailablePhase`
  or add a dedicated `TestBundleReconciler_HandleNew_RequeueAfterFloor` test is the
  implementer's choice. The existing test is the natural home for this assertion.
- The test may use the fake client (`sigs.k8s.io/controller-runtime/pkg/client/fake`)
  as all other bundle reconciler tests do.

---

## Zone 3 — Scoped out

- Changing the 500ms value in production code (not in scope here).
- Adding timeouts for other reconcilers (separate issues).
- Testing the exact value of `RequeueAfter` for non-Available-phase transitions.
