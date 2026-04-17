// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// useKeyboardShortcuts.ts — Global keyboard shortcut handler (#746).
//
// Wires three shortcuts onto the document:
//   ?    open/close the keyboard shortcuts help panel
//   r    trigger a manual data refresh
//   Esc  close the currently open side panel (node detail or bundle diff)
//
// All shortcuts are suppressed when an input, textarea, or contenteditable
// element has focus so that users can type normally without triggering actions.

import { useEffect, useCallback } from 'react'

/** Returns true when the event target is a text-input-like element. */
function isInputFocused(e: KeyboardEvent): boolean {
  const target = e.target as HTMLElement | null
  if (!target) return false
  const tag = (target.tagName ?? '').toLowerCase()
  if (tag === 'input' || tag === 'textarea') return true
  if (target.isContentEditable) return true
  return false
}

export interface KeyboardShortcutHandlers {
  /** Called when ? is pressed (toggle shortcuts panel). */
  onHelp: () => void
  /** Called when r is pressed (manual refresh). */
  onRefresh: () => void
  /** Called when Esc is pressed (close open panel). */
  onEscape: () => void
}

/**
 * useKeyboardShortcuts attaches a document-level keydown listener for the
 * three global shortcuts: ?, r, Esc. The listener is cleaned up on unmount.
 *
 * Input/textarea focus suppresses all shortcuts.
 */
export function useKeyboardShortcuts(handlers: KeyboardShortcutHandlers): void {
  const { onHelp, onRefresh, onEscape } = handlers

  const handleKeyDown = useCallback(
    (e: KeyboardEvent) => {
      if (isInputFocused(e)) return

      switch (e.key) {
        case '?':
          // Prevent '?' from being typed if focus shifts to an input elsewhere.
          e.preventDefault()
          onHelp()
          break
        case 'r':
        case 'R':
          e.preventDefault()
          onRefresh()
          break
        case 'Escape':
          onEscape()
          break
      }
    },
    [onHelp, onRefresh, onEscape],
  )

  useEffect(() => {
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [handleKeyDown])
}
