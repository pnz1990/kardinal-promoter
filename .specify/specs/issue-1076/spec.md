# Spec: issue-1076 — GoNativeType nil CEL value sentinel

## Design reference
- **Design doc**: `docs/design/39-demo-e2e-reliability.md`
- **Section**: `§ Future`
- **Implements**: `GoNativeType` returns `nil, nil` for a nil CEL value — ambiguous for callers (🔲 → ✅)

---

## Zone 1 — Obligations (falsifiable)

**O1** — `GoNativeType(nil)` MUST return `(nil, ErrNilCELValue)` instead of `(nil, nil)`.
Violation: calling `GoNativeType(nil)` and observing `err == nil`.

**O2** — `ErrNilCELValue` MUST be a package-level sentinel error in `pkg/cel/conversion`.
Violation: `errors.Is(err, conversion.ErrNilCELValue)` returns false when `GoNativeType(nil)` returns its error.

**O3** — `GoNativeType` with a non-nil CEL null value (`types.NullType`) MUST still return `(nil, nil)`.
This preserves the semantics of "CEL expression explicitly returned null" vs "evaluator passed nil".
Violation: `GoNativeType(types.NullValue)` returns a non-nil error.

**O4** — Unit tests MUST cover: `v==nil` → `ErrNilCELValue`, and `types.NullValue` → `(nil, nil)`.
Violation: no test file in `pkg/cel/conversion/` covering these two cases.

**O5** — `json.marshal` in `pkg/cel/library/json.go` already handles `(nil, err)` from `GoNativeType` by returning `types.NewErr(...)` — this MUST continue to work correctly when `GoNativeType(nil)` now returns an error.
Violation: `json.marshal` in CEL panics or silently swallows the error after this change.

---

## Zone 2 — Implementer's judgment

- Error message wording in `ErrNilCELValue` (e.g. "nil ref.Val: caller passed nil CEL value to GoNativeType")
- Whether to add a `//nolint` comment on `ErrNilCELValue`
- Test table structure

---

## Zone 3 — Scoped out

- Changing the `evaluate()` function in `pkg/reconciler/policygate/cel_evaluator.go` — the evaluator never calls `GoNativeType` directly; it uses `prg.Eval` which never returns nil ref.Val in practice.
- Changing behavior for `types.OptionalType` with no value — that already returns `(nil, nil)` intentionally (Optional with no value = falsy).
- Any change to how callers other than `json.marshal` handle the error — the only production caller is `marshalJSON` in `pkg/cel/library/json.go` which already handles err != nil correctly.
