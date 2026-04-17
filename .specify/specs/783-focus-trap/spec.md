# Spec: Focus trap in keyboard shortcuts modal (#783)

## Design reference
- **Design doc**: `docs/design/06-kardinal-ui.md`
- **Section**: §Future
- **Implements**: Focus trap in keyboard shortcuts modal (full WCAG compliance)

---

## Zone 1 — Obligations (falsifiable)

O1. When the modal opens, focus moves to the close button (the first focusable element).

O2. While the modal is open, pressing Tab from the last focusable element moves focus
    to the first focusable element (wraps forward).

O3. While the modal is open, pressing Shift+Tab from the first focusable element moves
    focus to the last focusable element (wraps backward).

O4. When the modal closes (by Esc, ✕ button, or backdrop click), focus returns to the
    element that had focus before the modal opened (trigger element).

O5. The modal container has `role="dialog"` and `aria-modal="true"` and `aria-label="Keyboard shortcuts"`.
    (Already implemented — must be preserved.)

---

## Zone 2 — Implementer's judgment

- Implementation strategy: `useRef` on the modal container + a `useEffect` that
  queries all focusable elements within the container on mount.
- Focusable elements selector: `button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])`.
- Avoid importing third-party focus-trap libraries — implement directly (the modal is simple).

---

## Zone 3 — Scoped out

- Screen reader announcement when modal opens (ARIA live region) — future work.
- Preventing scroll of the body when modal is open.
- Multiple simultaneous modals.
