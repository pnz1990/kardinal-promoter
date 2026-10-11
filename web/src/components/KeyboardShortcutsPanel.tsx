// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// KeyboardShortcutsPanel.tsx — Modal showing all keyboard shortcuts (#746).
//
// Triggered by pressing ? anywhere in the app (except when an input has focus).
// Closed by pressing ? again or Esc.
//
// #783: Focus trap — Tab/Shift+Tab cycles within the modal. Focus returns to the
// trigger element on close (WCAG 2.1 §2.1.2, §2.4.3). See useModalFocus.
// Global shortcuts are off while the panel is open, so ? is handled here.

import { useRef } from 'react'
import { useModalFocus } from '../useModalFocus'
import '../ui'
import '../styles/KeyboardShortcutsPanel.css'

interface ShortcutRow {
  key: string
  description: string
}

const SHORTCUTS: ShortcutRow[] = [
  { key: '⌘K / Ctrl+K', description: 'Go to a pipeline, environment or version, or run an action' },
  { key: '/', description: 'Focus pipeline search' },
  { key: '?', description: 'Show / hide this help panel' },
  { key: 'r', description: 'Refresh data now' },
  { key: 'j / k', description: 'Next / previous environment; on Approvals, next / previous approval' },
  { key: 'a', description: 'Approve: the open environment, or the approval in focus' },
  { key: 's', description: 'Ship a version of the open pipeline' },
  { key: 'Esc', description: 'Close the open side panel' },
]

interface KeyboardShortcutsPanelProps {
  onClose: () => void
}

export function KeyboardShortcutsPanel({ onClose }: KeyboardShortcutsPanelProps) {
  const panelRef = useRef<HTMLDivElement>(null)

  useModalFocus(panelRef, onClose)

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="Keyboard shortcuts"
      className="k-scrim"
      onClick={e => {
        // Close on backdrop click
        if (e.target === e.currentTarget) onClose()
      }}
      onKeyDown={e => {
        // Stop it here: in a browser the panel is gone by the time the event
        // reaches the page's ? shortcut, which would open the panel again.
        if (e.key === '?') { e.preventDefault(); e.stopPropagation(); onClose() }
      }}
    >
      <div ref={panelRef} className="k-dialog shortcuts">
        {/* Header */}
        <div className="shortcuts__header">
          <span className="shortcuts__title">KEYBOARD SHORTCUTS</span>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close keyboard shortcuts"
            className="k-icon-button k-icon-button--sm"
          >
            ✕
          </button>
        </div>

        {/* Shortcut rows */}
        <table className="shortcuts__table">
          <tbody>
            {SHORTCUTS.map(({ key, description }) => (
              <tr key={key}>
                <td className="shortcuts__key">
                  <kbd className="k-kbd">{key}</kbd>
                </td>
                <td className="shortcuts__description">{description}</td>
              </tr>
            ))}
          </tbody>
        </table>

        {/* Footer hint */}
        <div className="shortcuts__footer">
          Press <kbd className="shortcuts__hint-key">?</kbd> or{' '}
          <kbd className="shortcuts__hint-key">Esc</kbd> to close
        </div>
      </div>
    </div>
  )
}
