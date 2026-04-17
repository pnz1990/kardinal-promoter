# Spec: Global Keyboard Shortcuts (#746)

## Zone 1 — Obligations (falsifiable)

1. **`?` opens help modal**: Pressing `?` when focus is NOT in an input/textarea opens a
   `KeyboardShortcutsPanel` modal listing all shortcuts. If the panel is already open, pressing
   `?` closes it.

2. **`r` triggers refresh**: Pressing `r` when focus is NOT in an input/textarea calls the same
   data-refresh function as the existing refresh button (same `doFetchAll` codepath).

3. **`Esc` closes open panels**: Pressing `Esc` closes the first open panel in priority order:
   `KeyboardShortcutsPanel` → `BundleDiffPanel` → `NodeDetail`. Only one closes per keypress.

4. **Input suppression**: All shortcuts are suppressed when `document.activeElement` is an
   `input`, `textarea`, `select`, or `[contenteditable]` element.

5. **`KeyboardShortcutsPanel` content**: The modal lists at minimum: `?`, `r`, `Esc` with their
   descriptions. Renders as a dialog-like overlay with a close button.

6. **Tests**: Each shortcut has at least one vitest test verifying activation (and suppression
   in input context).

## Zone 2 — Implementer's judgment

- Whether to use `useCallback` or extract a custom hook `useKeyboardShortcuts`
- Modal styling (reuse existing overlay patterns from `BundleDiffPanel`)
- Whether `/` is implemented: issue mentions it but no AC requires it; omit if no search field

## Zone 3 — Scoped out

- `/` search shortcut (no search field exists in the current UI)
- Focus trap in the shortcuts modal (WCAG full compliance deferred to #748)
- Custom keybinding configuration
