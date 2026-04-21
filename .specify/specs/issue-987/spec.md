# Spec: Fix `RequeueAfter: time.Millisecond` hot loop in bundle reconciler

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future`
- **Implements**: Replace `RequeueAfter: time.Millisecond` with a safe minimum floor (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

O1. `pkg/reconciler/bundle/reconciler.go` must not use `RequeueAfter: time.Millisecond`
    anywhere. Violation: `grep 'RequeueAfter: time.Millisecond'` returns any result.

O2. The replacement value must be `RequeueAfter: 500*time.Millisecond`.
    Violation: the constant replacing `time.Millisecond` is anything less than 500ms.

O3. The design doc `docs/design/15-production-readiness.md` must move the
    `RequeueAfter: time.Millisecond` hot loop item from `🔲 Future` to `✅ Present`.
    Violation: the item still appears under `## Future`.

O4. The build must pass (`go build ./...`). Violation: any build error.

O5. All tests must pass (`go test ./... -race -count=1 -timeout 120s`). Violation: any failing test.

O6. The comment explaining the requeue must accurately describe the new value.
    Violation: comment still says "immediately" or references the old 1ms value.

---

## Zone 2 — Implementer's judgment

- Whether to define a named constant (e.g. `const availableRequeueAfter = 500*time.Millisecond`)
  or use the literal inline.
- Whether to add a debug log statement noting the requeue delay.

---

## Zone 3 — Scoped out

- Changing any other `RequeueAfter` values in the bundle reconciler or other reconcilers.
- Introducing any configurable/dynamic delay value.
- Changes to other reconciler packages.
