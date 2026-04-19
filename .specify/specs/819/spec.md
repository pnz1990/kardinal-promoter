# Spec: test(ui): aria-pressed test for PipelineList virtual mode selected state

Issue: #819
Date: 2026-04-18
Author: sess-1f914935

## Design reference
- N/A — test-only improvement, no user-visible behavior change

---

## Zone 1 — Obligations (falsifiable)

O1. In the virtual scrolling test suite (`describe('PipelineList — virtual scrolling')`),
    there is a test that selects a pipeline and verifies `aria-pressed="true"` on the
    selected button, specifically when using the virtual rendering path (>50 pipelines).
    Violation: test missing, or test uses ≤50 pipelines and falls through to normal rendering.

O2. The test renders ≥100 pipelines (to activate the virtual path), selects one by
    calling `onSelect`, and then queries `screen.getAllByRole('button', {pressed: true})`.
    Violation: test queries for aria-pressed without rendering >50 pipelines.

O3. All existing tests in the file continue to pass.
    Violation: any existing test fails.

---

## Zone 2 — Implementer's judgment

- Which pipeline to select (any index is fine — choose index 0 for simplicity)
- Whether to use `userEvent.click` or direct `fireEvent.click`
- In jsdom, virtual items may not render unless `listContainerRef.current` has a height;
  test the selection state through the selected prop rather than DOM scroll behavior

---

## Zone 3 — Scoped out

- Actually scrolling the virtual list in jsdom
- Testing aria-pressed after scroll position change (jsdom has no layout)
