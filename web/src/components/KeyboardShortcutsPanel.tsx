// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// KeyboardShortcutsPanel.tsx — Modal showing all keyboard shortcuts (#746).
//
// Triggered by pressing ? anywhere in the app (except when an input has focus).
// Closed by pressing ? again or Esc.

interface ShortcutRow {
  key: string
  description: string
}

const SHORTCUTS: ShortcutRow[] = [
  { key: '?', description: 'Show / hide this help panel' },
  { key: 'r', description: 'Refresh data now' },
  { key: 'Esc', description: 'Close the open side panel' },
]

interface KeyboardShortcutsPanelProps {
  onClose: () => void
}

export function KeyboardShortcutsPanel({ onClose }: KeyboardShortcutsPanelProps) {
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
