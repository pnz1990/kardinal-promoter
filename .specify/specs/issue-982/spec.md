# Spec: Populate Bundle status.conditions on phase transitions

## Design reference
- **Design doc**: `docs/design/15-production-readiness.md`
- **Section**: `§ Future` (Lens 6)
- **Implements**: Bundle `status.conditions` are declared but never populated (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

O1. After `handleNew` sets phase to `Available`, `Bundle.status.conditions` MUST contain a
    `Ready` condition with `status=False` and `reason=Available`.
    Violation: `kubectl describe bundle <name>` after creation shows `Conditions: <none>`
    or `Ready` is absent or has `status=True`.

O2. After `handleAvailable` sets phase to `Promoting`, `Bundle.status.conditions` MUST
    contain a `Ready` condition with `status=False` and `reason=Promoting`.
    Violation: conditions unchanged from Available when phase becomes Promoting.

O3. After `handleAvailable` sets phase to `Failed` (translator error), `Bundle.status.conditions`
    MUST contain a `Ready` condition with `status=False` and `reason=Failed`, AND a
    `Failed` condition with `status=True`.
    Violation: no `Failed` condition when phase is Failed.

O4. After `markSuperseded` sets phase to `Superseded`, `Bundle.status.conditions` MUST
    contain a `Ready` condition with `status=False` and `reason=Superseded`.
    Violation: conditions unchanged after Superseded.

O5. The `setBundleCondition` helper MUST update an existing condition (same Type) in-place
    rather than appending a duplicate.
    Violation: two conditions with the same `Type` in `status.conditions`.

O6. All four phase transitions use the same `setBundleCondition` helper (no duplicated logic).
    Violation: phase transition sets conditions without calling `setBundleCondition`.

O7. The design doc `docs/design/15-production-readiness.md` must move this item from
    `🔲 Future` to `✅ Present`.
    Violation: item still appears as `🔲` in the Future section.

O8. Build passes (`go build ./...`). Violation: any build error.

O9. All tests pass (`go test ./... -race -count=1 -timeout 120s`). Violation: any failure.

O10. New table-driven test cases exist that assert conditions are set after Available,
     Promoting, Failed, and Superseded transitions.
     Violation: no test cases covering conditions.

---

## Zone 2 — Implementer's judgment

- Whether to also set conditions during Verified phase (via handleSyncEvidence).
  If added, `Ready=True, reason=Verified` would enable `kubectl wait --for=condition=Ready`.
- Whether to set a `Promoting` condition type in addition to updating the `Ready` condition.
- Exact message strings (descriptive, not blocking).

---

## Zone 3 — Scoped out

- Populating conditions on PromotionStep resources (already done in existing code).
- Adding new CRD fields beyond `status.conditions` (already declared in bundle_types.go).
- Changes to non-bundle reconcilers.
