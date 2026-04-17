# Spec: WCAG 2.1 AA Color Contrast Audit (#757)

## Zone 1 — Obligations (falsifiable)

1. **Zero color-contrast violations** in the axe-core accessibility test (Journey 009)
   after this PR merges. The `color-contrast` rule can be removed from `KNOWN_DISABLE`.
   _Violation_: axe reports any `color-contrast` violation.

2. **All status colors in PipelineList** use CSS variables (`var(--color-success)`,
   `var(--color-accent)`, etc.) rather than hardcoded hex values.
   _Violation_: `phaseColor` map in PipelineList.tsx contains any hardcoded hex.

3. **Dark mode `--color-text-faint`** has ≥ 4.5:1 contrast ratio on `#0c1628` and `#1e293b`.
   _Violation_: contrast ratio below 4.5:1 on either dark surface.

4. **Light mode `--color-text-faint`** has ≥ 4.5:1 contrast ratio on `#f1f5f9`.
   _Violation_: contrast ratio below 4.5:1 on light bg.

5. **Light mode `--color-success`** has ≥ 4.5:1 contrast ratio on `#f1f5f9`.
   _Violation_: contrast ratio below 4.5:1.

6. **Light mode `--color-accent`** has ≥ 4.5:1 contrast ratio on `#f1f5f9`.
   _Violation_: contrast ratio below 4.5:1.

7. **All 314 unit tests pass.** No regressions.
   _Violation_: any test fails.

8. **Apache 2.0 header** on new files. No new files added.

## Zone 2 — Implementer's Judgment

- Whether to update CSS variables or inline styles (variables preferred)
- Exact shade selections that are WCAG-compliant and visually consistent

## Zone 3 — Scoped Out

- `nested-interactive` violations (tracked in #758)
- Manual color contrast audit of components not exercised by the E2E test
- WCAG AAA (7:1) compliance — only AA (4.5:1) required
- Full audit of all 206 hardcoded hex values (PRs #738 already migrated most)
