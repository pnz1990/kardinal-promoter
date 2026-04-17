# Spec: Enable color-contrast and nested-interactive axe rules in Journey 009 (#761, #762)

## Zone 1 — Obligations (falsifiable)

1. **`color-contrast` removed from `KNOWN_CONTRAST_DISABLE`** in Journey 009.
   _Violation_: `color-contrast` still appears in `DISABLED_RULES`.

2. **`nested-interactive` removed from `KNOWN_SVG_DISABLE`** in Journey 009.
   _Violation_: `nested-interactive` still appears in `DISABLED_RULES`.

3. **DAG SVG `nested-interactive` fixed**: the `<text role="link">` PR badge inside
   `<g role="button">` must not carry `role="link"` (which axe treats as interactive).
   The PR link remains clickable via `onClick` and discoverable via `aria-label` on the `<g>`.
   _Violation_: axe reports `nested-interactive` on any DAG node with a PR badge.

4. **No new axe violations introduced**: removing both rules from DISABLED_RULES must
   not cause the Journey 009 E2E tests to fail due to other violations.
   _Violation_: `bun run test` or E2E journey 009 fails after this change.

5. **No TypeScript errors** after the change.
   _Violation_: tsc --noEmit fails.

6. **Apache 2.0 header** preserved on all modified files.
   _Violation_: Header missing or removed.

## Zone 2 — Implementer's Judgment

- Whether to also remove the `role="link"` attribute from the PR text or replace with
  an aria-description approach.
- Whether to keep `KNOWN_CONTRAST_DISABLE` and `KNOWN_SVG_DISABLE` as empty arrays or
  remove them entirely (simplify to a single inline array).

## Zone 3 — Scoped Out

- Fixing any other axe violations not related to color-contrast or nested-interactive.
- Redesigning the DAG PR badge appearance.
- Adding keyboard focus ring to DAG text PR badge.
