# Spec: Color Contrast Audit (#757)

## Zone 1 — Obligations (falsifiable)

1. **axe-core passes without color-contrast disabled**: The 009-accessibility.spec.ts
   journey tests must pass with `color-contrast` removed from `DISABLED_RULES`. Both
   scenarios (initial load and pipeline-selected) must have 0 violations.

2. **No visual regression**: All existing vitest unit tests continue to pass (314 tests).

3. **Theme CSS updated**: `theme.css` has contrast-compliant values for all text color
   variables in both dark and light themes.

## Zone 2 — Implementer's judgment

- Which specific CSS variables to change (verified by running axe-core)
- Whether to adjust the background or the text color to achieve compliance
- Which hardcoded hex colors need updating in component files

## Zone 3 — Scoped out

- Design system overhaul / visual redesign
- Status colors that only appear at larger font sizes (>18px or bold >14px require only 3:1)
