# Spec: feat(ui): / keyboard shortcut to focus pipeline search input

Issue: #800
Date: 2026-04-19
Author: sess-1ccc6100

## Design reference
- **Design doc**: `docs/design/06-kardinal-ui.md`
- **Section**: `§ Future`
- **Implements**: `/` keyboard shortcut for search (🔲 → ✅) — epic #587

---

## Zone 1 — Obligations (falsifiable)

O1. Pressing `/` when no input/textarea/contenteditable has focus moves keyboard focus
    to the pipeline filter input. Violation: focus remains elsewhere.

O2. Pressing `/` when any input, textarea, or contenteditable has focus does NOT
    intercept the keystroke — the `/` character types normally. Violation: `/` is
    swallowed inside an input.

O3. Pressing `Esc` while the pipeline filter input has focus clears the filter value
    and blurs the input. Violation: Esc does not clear or does not blur.

O4. The `/` shortcut is listed in the `?` keyboard shortcuts help modal with the
    description "Focus pipeline search". Violation: modal opens without this entry.

O5. The filter input renders at all pipeline counts (not just when >3) so the shortcut
    always works. Violation: `/` pressed with ≤3 pipelines does nothing.

O6. Filter input has an accessible label (`aria-label` or `<label for>`). Violation:
    axe-core reports missing label.

O7. `docs/design/06-kardinal-ui.md` § Future item `/ keyboard shortcut for search`
    is moved from 🔲 to ✅ Present with `(PR #N, date)`. Violation: design doc not updated.

---

## Zone 2 — Implementer's judgment

- Whether to use a forwarded ref, callback ref, or context for the focus mechanism
  (forwarded ref preferred — avoids prop drilling through FleetHealthBar)
- Whether to always show the filter input or only show it when pipelines exist
  (always show when pipelines.length >= 0 is safest for O5; hide when pipelinesLoading)
- Exact wording of the keyboard shortcut description in the help modal

---

## Zone 3 — Scoped out

- Search across non-pipeline entities (bundles, policy gates)
- Server-side search (client-side filter only)
- Persistent search query in URL hash fragment
- `PipelineOpsTable` search integration (separate issue)
