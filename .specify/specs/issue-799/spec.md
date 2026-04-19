# Spec: feat(ui): responsive layout at 1280px width

## Design reference
- **Design doc**: `docs/design/06-kardinal-ui.md`
- **Section**: `§ Future`
- **Implements**: Responsive layout at 1280px width (🔲 → ✅) — epic #587

## Zone 1 — Obligations

O1. At 1280×800 viewport, the root layout MUST NOT produce horizontal document overflow
    (i.e. document.documentElement.scrollWidth ≤ 1280).

O2. The `aside` sidebar MUST shrink to a minimum width at narrow viewports rather than
    forcing overflow; it already has `minWidth: 200px` which satisfies this.

O3. The `main` content area MUST use `overflow: hidden` (already set) so inner overflow
    is contained rather than expanding the document.

O4. A Playwright E2E test (`web/test/e2e/journeys/010-responsive-layout.spec.ts`) MUST
    verify that at 1280px viewport width, `document.documentElement.scrollWidth` ≤ 1280
    in both the pipeline-list-only state and the pipeline-selected state.

O5. Design doc updated: `docs/design/06-kardinal-ui.md` (🔲 → ✅).

## Zone 2 — Implementer's judgment

- The actual layout is already responsive by construction (flexbox, sidebar=240px,
  main=flex:1). The main work is writing the test to verify this claim and fixing
  any edge cases the test reveals.
- If the test reveals actual overflow, the fix approach: add `maxWidth: '100%'` or
  `overflow: hidden` to the offending container.

## Zone 3 — Scoped out

- Mobile layouts (< 768px) — not in epic #587 scope
- Virtualization for 50+ pipeline entries — separate issue
