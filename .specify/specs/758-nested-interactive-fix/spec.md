# Spec: Fix nested-interactive in PipelineLaneView (#758)

## Zone 1 — Obligations (falsifiable)

1. **No `nested-interactive` axe violation** in the accessibility test (Journey 009)
   after this change. The test suite passes with 0 blocking violations.
   _Violation_: axe reports `nested-interactive` on any stage card.

2. **Stage card selection still works with mouse**: clicking anywhere on the card
   (not on the PR link, Promote button, or Rollback button) calls `onSelectNode`.
   _Violation_: clicking the card background does not select the stage.

3. **Stage card selection works via keyboard**: the card is navigable and selectable
   via Tab + Enter from keyboard without requiring a mouse.
   _Violation_: no keyboard path reaches the card's select action.

4. **PR link, Promote, Rollback buttons remain independently focusable** via Tab.
   They do not require clicking the card first.
   _Violation_: any of these buttons become unfocusable after the change.

5. **No TypeScript errors** after the change.
   _Violation_: tsc --noEmit fails.

6. **Apache 2.0 header** on all changed files (no new files added).
   _Violation_: Header missing.

## Zone 2 — Implementer's Judgment

- Whether to use a visually-hidden "Select" button or make the card label clickable
- Whether to remove `role="button"` from the outer div or keep it
- ARIA label strategy for the "select" button

## Zone 3 — Scoped Out

- Color contrast violations (tracked in #757)
- PipelineList nested-interactive (different component, different issue)
- Focus trap within the card
- Redesigning the card layout
