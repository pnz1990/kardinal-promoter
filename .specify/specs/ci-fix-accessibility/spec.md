# Spec: fix(ui): WCAG 2.1 AA violations after Journey 009 rule enablement

## Zone 1 — Obligations (falsifiable)

O1. Journey 009 default dashboard state test passes (0 violations).
    - nested-interactive: CopyButton is NOT a descendant of the selection button.
    - aria-allowed-attr: No element has aria-selected on role=button.

O2. Journey 009 pipeline-selected state test passes (0 violations).
    - color-contrast: no element with hardcoded #7dd3fc or #cbd5e1 or var(--color-text)
      on a fixed dark background appears on a light-mode page background.
    - aria-allowed-attr: resolved as above.

O3. Journey 001 Step 3 "Selected pipeline is highlighted in sidebar" test passes.
    - Evidence: pipeline selection button has aria-current="true" when selected.
    - Test updated: [aria-current="true"] instead of [aria-selected="true"].

O4. All 336 existing unit tests continue to pass.

O5. TypeScript tsc --noEmit clean.

## Zone 2 — Implementer's judgment

- Position of the extracted CopyButton div.
- Value of --color-code-text in each theme.
- Using aria-current="true" instead of aria-current="page" (both are valid; "true" is generic).

## Zone 3 — Scoped out

- CopyButton positioning in multi-namespace grouped view (cosmetic).
- Bundle chip unselected text color (#64748b) contrast on dark chip background (4.4:1).

## Design reference

- N/A — infrastructure/accessibility fix with no new user-visible behavior.
