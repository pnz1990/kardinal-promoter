# Spec: fix(ui): WCAG 2.1 AA violations after Journey 009 rule enablement

## Zone 1 — Obligations (falsifiable)

O1. Journey 009 default dashboard state test passes (0 violations).
    - Violation: `nested-interactive` no longer fires on PipelineList items.
    - Evidence: `CopyButton` is not a descendant of the selection `<button>`.

O2. Journey 009 pipeline-selected state test passes (0 violations).
    - Violation: `color-contrast` no longer fires on `#7dd3fc`-colored elements in light mode.
    - Evidence: all previously hardcoded `#7dd3fc` colors replaced with `var(--color-code-text)`,
      which resolves to `#0369a1` (sky-700, 5.5:1 on `#f1f5f9`) in light mode.

O3. Journey 001 Step 3 "Selected pipeline is highlighted in sidebar" test passes.
    - Violation: `[aria-selected="true"]` not found after clicking a pipeline.
    - Evidence: pipeline selection `<button>` has `aria-selected={selected === p.name}`.

O4. All 336 existing unit tests continue to pass.

O5. TypeScript `--noEmit` clean.

## Zone 2 — Implementer's judgment

- Position of the extracted CopyButton relative to the pipeline name span.
- Exact value of `--color-code-text` in each theme (sky-300 / sky-700).

## Zone 3 — Scoped out

- CopyButton visibility in multi-namespace grouped view (cosmetic, not WCAG).
- CopyButton positioning in very narrow sidebar widths.

## Design reference

- N/A — infrastructure/accessibility fix with no new user-visible behavior.
  (The copy button functionality and pipeline selection are unchanged; only the
  DOM structure and a CSS variable are modified to comply with WCAG 2.1 AA.)
