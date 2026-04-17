// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// KeyboardShortcutsPanel.tsx — Modal showing all keyboard shortcuts (#746).
//
// Triggered by pressing ? anywhere in the app (except when an input has focus).
// Closed by pressing ? again or Esc.
//
// #783: Focus trap — Tab/Shift+Tab cycles within the modal. Focus returns to the
// trigger element on close (WCAG 2.1 §2.1.2, §2.4.3).

import { useRef, useEffect } from 'react'

interface ShortcutRow {
  key: string
  description: string
}

const SHORTCUTS: ShortcutRow[] = [
  { key: '?', description: 'Show / hide this help panel' },
  { key: 'r', description: 'Refresh data now' },
  { key: 'Esc', description: 'Close the open side panel' },
]

/** Selector for all naturally focusable elements within a container. */
const FOCUSABLE =
  'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'

interface KeyboardShortcutsPanelProps {
  onClose: () => void
}

export function KeyboardShortcutsPanel({ onClose }: KeyboardShortcutsPanelProps) {
  const panelRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    // Capture the element that had focus before the modal opened.
    const previouslyFocused = document.activeElement as HTMLElement | null

    // Move focus to the close button (first focusable element) on mount.
    if (panelRef.current) {
      const first = panelRef.current.querySelectorAll<HTMLElement>(FOCUSABLE)[0]
      first?.focus()
    }

    // Focus trap: intercept Tab / Shift+Tab to cycle within the modal.
    const handleKeyDown = (e: KeyboardEvent) => {
      if (!panelRef.current) return
      if (e.key !== 'Tab') return

      const focusable = Array.from(
        panelRef.current.querySelectorAll<HTMLElement>(FOCUSABLE),
      ).filter(el => !el.hasAttribute('disabled'))

      if (focusable.length === 0) return

      const first = focusable[0]
      const last = focusable[focusable.length - 1]

      if (e.shiftKey) {
        // Shift+Tab: wrap backward from first to last
        if (document.activeElement === first) {
          e.preventDefault()
          last.focus()
        }
      } else {
        // Tab: wrap forward from last to first
        if (document.activeElement === last) {
          e.preventDefault()
          first.focus()
        }
      }
    }

    document.addEventListener('keydown', handleKeyDown)

    return () => {
      document.removeEventListener('keydown', handleKeyDown)
      // Return focus to the element that had it before the modal opened.
      previouslyFocused?.focus()
    }
  }, [])

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="Keyboard shortcuts"
      style={{
        position: 'fixed',
        inset: 0,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        background: 'rgba(0,0,0,0.45)',
        zIndex: 1000,
      }}
      onClick={e => {
        // Close on backdrop click
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        ref={panelRef}
        style={{
          background: 'var(--color-surface)',
          border: '1px solid var(--color-border)',
          borderRadius: '8px',
          padding: '1.25rem 1.5rem',
          minWidth: '280px',
          maxWidth: '400px',
          boxShadow: '0 8px 32px rgba(0,0,0,0.25)',
        }}
      >
        {/* Header */}
        <div style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: '1rem',
        }}>
          <span style={{
            fontWeight: 600,
            fontSize: '0.85rem',
            color: 'var(--color-text)',
            letterSpacing: '0.04em',
          }}>
            KEYBOARD SHORTCUTS
          </span>
          <button
            onClick={onClose}
            aria-label="Close keyboard shortcuts"
            style={{
              background: 'none',
              border: 'none',
              cursor: 'pointer',
              fontSize: '0.9rem',
              color: 'var(--color-text-muted)',
              lineHeight: 1,
              padding: '2px 4px',
            }}
          >
            ✕
          </button>
        </div>

        {/* Shortcut rows */}
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <tbody>
            {SHORTCUTS.map(({ key, description }) => (
              <tr key={key}>
                <td style={{
                  paddingBottom: '0.5rem',
                  paddingRight: '1rem',
                  verticalAlign: 'top',
                  width: '3rem',
                }}>
                  <kbd style={{
                    display: 'inline-block',
                    background: 'var(--color-surface-raised)',
                    border: '1px solid var(--color-border)',
                    borderRadius: '4px',
                    padding: '1px 6px',
                    fontSize: '0.75rem',
                    fontFamily: 'monospace',
                    color: 'var(--color-text)',
                    minWidth: '1.5rem',
                    textAlign: 'center',
                  }}>
                    {key}
                  </kbd>
                </td>
                <td style={{
                  paddingBottom: '0.5rem',
                  fontSize: '0.8rem',
                  color: 'var(--color-text-muted)',
                  verticalAlign: 'top',
                }}>
                  {description}
                </td>
              </tr>
            ))}
          </tbody>
        </table>

        {/* Footer hint */}
        <div style={{
          marginTop: '0.75rem',
          paddingTop: '0.75rem',
          borderTop: '1px solid var(--color-border-muted)',
          fontSize: '0.7rem',
          color: 'var(--color-text-faint)',
        }}>
          Press <kbd style={{ fontFamily: 'monospace' }}>?</kbd> or{' '}
          <kbd style={{ fontFamily: 'monospace' }}>Esc</kbd> to close
        </div>
      </div>
    </div>
  )
}
