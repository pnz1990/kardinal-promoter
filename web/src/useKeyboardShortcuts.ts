// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// useKeyboardShortcuts.ts — Global keyboard shortcut handler (#746).
//
// Wires four shortcuts onto the document:
//   ?    open/close the keyboard shortcuts help panel
//   r    trigger a manual data refresh
//   Esc  close the currently open side panel (node detail or bundle diff)
//   /    focus the pipeline search input (#800)
//
// All shortcuts are suppressed when an input, textarea, select or
// contenteditable element has focus, when Ctrl/Cmd/Alt is held (so browser
// shortcuts such as Ctrl+R keep working), and while a modal dialog is open
// (the dialog handles its own keys).

import { useEffect, useCallback } from 'react'

/** Returns true when the event target is a form field or editable element. */
function isInputFocused(e: KeyboardEvent): boolean {
  const target = e.target as HTMLElement | null
  if (!target) return false
  const tag = (target.tagName ?? '').toLowerCase()
  if (tag === 'input' || tag === 'textarea' || tag === 'select') return true
  if (target.isContentEditable) return true
  return false
}

/** Returns true while an aria-modal dialog is on screen. */
function isModalOpen(): boolean {
  return document.querySelector('[aria-modal="true"]') !== null
}

export interface KeyboardShortcutHandlers {
  /** Called when ? is pressed (toggle shortcuts panel). */
  onHelp: () => void
  /** Called when r is pressed (manual refresh). */
  onRefresh: () => void
  /** Called when Esc is pressed (close open panel). */
  onEscape: () => void
  /** Called when / is pressed (focus pipeline search input). #800 */
  onSearch?: () => void
}

/**
 * useKeyboardShortcuts attaches a document-level keydown listener for the
 * four global shortcuts: ?, r, Esc, /. The listener is cleaned up on unmount.
 *
 * A focused form field suppresses every shortcut, Esc included: the field
 * handles its own Esc (PipelineList's search box clears its filter).
 */
export function useKeyboardShortcuts(handlers: KeyboardShortcutHandlers): void {
  const { onHelp, onRefresh, onEscape, onSearch } = handlers

  const handleKeyDown = useCallback(
    (e: KeyboardEvent) => {
      if (e.ctrlKey || e.metaKey || e.altKey) return
      if (isInputFocused(e)) return
      if (isModalOpen()) return

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
        case '/':
          // #800: focus the pipeline search input
          if (onSearch) {
            e.preventDefault()
            onSearch()
          }
          break
      }
    },
    [onHelp, onRefresh, onEscape, onSearch],
  )

  useEffect(() => {
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [handleKeyDown])
}
