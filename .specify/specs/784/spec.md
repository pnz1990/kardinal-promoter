# Spec: feat(ui): skeleton loading states for NodeDetail and BundleTimeline

## Design reference
- **Design doc**: `docs/design/06-kardinal-ui.md`
- **Section**: `## Future`
- **Implements**: 🔲 Skeleton loading states (replace blank panels) — moves to ✅

Note: PipelineList and DAGView skeleton states are already implemented (#335). This spec covers
the remaining components: NodeDetail stepLoading text → skeleton, BundleTimeline when loading.

---

## Zone 1 — Obligations (falsifiable)

O1. NodeDetail: when `stepLoading === true`, the panel MUST show animated shimmer skeleton
    rectangles instead of the text "Loading step details..." (italic grey text).

O2. BundleTimeline: when `loading === true` (prop added), the component MUST show animated
    shimmer skeleton chips instead of empty content.

O3. The skeleton animation style MUST be consistent with PipelineList and DAGView:
    - CSS animation: shimmer (background-position sweep, 1.5s infinite)
    - Colors: matching the existing `#1e293b` / `#293548` gradient in dark mode
    - Adapts to CSS `--color-bg-secondary` / `--color-bg-elevated` for theme compatibility

O4. Both skeleton implementations MUST be accessible: each skeleton element MUST have
    `aria-hidden="true"` to suppress screen reader noise for decorative loading shapes.
    A visually hidden text element with `role="status"` and appropriate text MUST be present.

O5. Tests MUST exist for both skeleton implementations:
    - NodeDetail: test that stepLoading=true renders skeleton elements (not the text string)
    - BundleTimeline: test that loading=true renders skeleton elements

O6. No new component file is needed — implement inline in NodeDetail.tsx and BundleTimeline.tsx.

---

## Zone 2 — Implementer's judgment

- Number of skeleton chips/rows to show (suggest 3-4 for NodeDetail, 4-5 for BundleTimeline)
- Exact widths/heights for skeleton shapes
- Whether to extract a shared `SkeletonRow` helper (acceptable but not required for 2 components)

---

## Zone 3 — Scoped out

- PipelineList skeleton (already done)
- DAGView skeleton (already done)
- Dark/light mode adaptation for skeleton colors (existing inline styles use dark-mode hardcoded values; theme token update is a separate concern)
- Global spinner or progress indicator
