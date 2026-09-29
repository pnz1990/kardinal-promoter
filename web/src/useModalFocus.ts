// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// useModalFocus.ts — keyboard behaviour shared by every aria-modal dialog:
// focus moves into the dialog when it opens, Tab and Shift+Tab stay inside it,
// Esc closes it, and focus returns to the element that opened it
// (WCAG 2.1 §2.1.2, §2.4.3).

import { useEffect, useRef, type RefObject } from 'react'

/** Selector for naturally focusable elements. */
export const FOCUSABLE =
  'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'

function focusableIn(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE))
    .filter(el => !el.hasAttribute('disabled'))
}

/**
 * Wire focus handling for a modal dialog.
 *
 * @param containerRef the element that holds the dialog's controls
 * @param onClose called when the user presses Esc
 * @param initialFocus optional element to focus first (defaults to the first focusable)
 */
export function useModalFocus(
  containerRef: RefObject<HTMLElement | null>,
  onClose: () => void,
  initialFocus?: RefObject<HTMLElement | null>,
): void {
  // Keep the latest onClose without re-running the effect (which would move
  // focus back to the first control on every parent render).
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose

  useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null
    const container = containerRef.current
    if (container) {
      const target = initialFocus?.current ?? focusableIn(container)[0]
      target?.focus()
    }

    const handleKeyDown = (e: KeyboardEvent) => {
      const el = containerRef.current
      if (!el) return
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        onCloseRef.current()
        return
      }
      if (e.key !== 'Tab') return
      const focusable = focusableIn(el)
      if (focusable.length === 0) {
        e.preventDefault()
        return
      }
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      const active = document.activeElement
      const inside = active instanceof Node && el.contains(active)
      if (e.shiftKey) {
        if (active === first || !inside) {
          e.preventDefault()
          last.focus()
        }
      } else if (active === last || !inside) {
        e.preventDefault()
        first.focus()
      }
    }

    // Capture phase so Esc reaches the dialog before any global shortcut handler.
    document.addEventListener('keydown', handleKeyDown, true)
    return () => {
      document.removeEventListener('keydown', handleKeyDown, true)
      if (previouslyFocused && document.contains(previouslyFocused)) previouslyFocused.focus()
    }
    // Run once per open: the dialog mounts when it opens and unmounts when it closes.
  }, [])
}
