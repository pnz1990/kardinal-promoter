# Spec: feat(ui): virtualization for pipeline list with 50+ entries

Issue: #815
Date: 2026-04-19
Author: sess-1ccc6100

## Design reference
- **Design doc**: `docs/design/06-kardinal-ui.md`
- **Section**: `§ Future`
- **Implements**: Virtualization for pipeline list with 50+ entries (🔲 → ✅) — epic #587

---

## Zone 1 — Obligations (falsifiable)

O1. When `pipelines.length > 50`, the list uses `@tanstack/react-virtual` to render
    only visible items. Violation: all 100+ items are in the DOM simultaneously.

O2. When `pipelines.length ≤ 50`, the list renders normally (no virtual window).
    Violation: virtual wrapper added even for small lists.

O3. Filter (search) continues to work with virtualization active — filtered results
    are virtualized correctly. Violation: search returns wrong results when virtual.

O4. The selected pipeline item has `aria-pressed=true` regardless of scroll position.
    Violation: selected state lost after scrolling.

O5. Multi-namespace grouped display is NOT virtualized (too complex with variable
    header heights). Falls back to normal rendering. Violation: grouped display broken.

O6. Design doc updated: 06-kardinal-ui.md (🔲 → ✅).

---

## Zone 2 — Implementer's judgment

- Container height: use a fixed-height container for the virtual list
- Item height estimate: ~52px per item (measured from design)
- Whether to use `useVirtualizer` from `@tanstack/react-virtual`

---

## Zone 3 — Scoped out

- Virtualization for multi-namespace grouped display
- Infinite scroll / server-side pagination
- Windows below 50 pipeline threshold
