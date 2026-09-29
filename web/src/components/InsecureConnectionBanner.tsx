// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/InsecureConnectionBanner.tsx — Warning banner when UI is accessed over HTTP
// from a non-localhost origin. Prompts users to use kubectl port-forward for in-cluster access.
//
// Design doc: docs/design/06-kardinal-ui.md §Future — port-forward UX (#913)

interface InsecureConnectionBannerProps {
  /** Whether the banner has been dismissed by the user. */
  dismissed: boolean
  /** Callback to dismiss the banner. */
  onDismiss: () => void
}

/** The command that reaches the UI on a default install (release kardinal-promoter). */
export const PORT_FORWARD_COMMAND = 'kubectl port-forward -n kardinal-system deploy/kardinal-promoter 8082:8082'

/** Loopback host names. `location.hostname` keeps the brackets on IPv6 ("[::1]"). */
const LOOPBACK_HOSTS = new Set(['localhost', '127.0.0.1', '[::1]', '::1'])

/**
 * Returns true when the page is accessed over plain HTTP from a non-localhost origin.
 * Port-forward to loopback (localhost, 127.0.0.1 or [::1]) is safe and is the
 * documented access method — it must NOT trigger the warning.
 */
export function isInsecureNonLocalConnection(): boolean {
  if (typeof window === 'undefined') return false
  if (window.location.protocol === 'https:') return false
  return !LOOPBACK_HOSTS.has(window.location.hostname)
}

/**
 * InsecureConnectionBanner renders an amber warning banner when the UI is accessed
 * over plain HTTP from a non-localhost address. It is dismissible for the session.
 */
export function InsecureConnectionBanner({ dismissed, onDismiss }: InsecureConnectionBannerProps) {
  if (dismissed) return null
  if (!isInsecureNonLocalConnection()) return null

  return (
    <div
      role="alert"
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        background: 'var(--color-warning-bg)',
        border: '1px solid var(--color-warning)',
        borderRadius: '6px',
        padding: '0.5rem 0.75rem',
        marginBottom: '0.75rem',
        gap: '0.75rem',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
        <span style={{ color: 'var(--color-warning)', fontSize: '0.9rem' }} aria-hidden="true">⚠</span>
        <span style={{ fontSize: '0.82rem', color: 'var(--color-warning)', fontWeight: 600 }}>
          Insecure connection — kardinal UI is accessed over plain HTTP.{' '}
          Use{' '}
          <code style={{ fontFamily: 'monospace', fontSize: '0.78rem' }}>
            {PORT_FORWARD_COMMAND}
          </code>{' '}
          and open{' '}
          <a href="http://localhost:8082/ui/" style={{ color: 'var(--color-warning)' }}>
            http://localhost:8082/ui/
          </a>
          {' '}for secure in-cluster access.
        </span>
      </div>
      <button
        onClick={onDismiss}
        aria-label="Dismiss insecure connection warning"
        style={{
          background: 'none',
          border: '1px solid var(--color-warning)',
          borderRadius: '4px',
          color: 'var(--color-warning)',
          cursor: 'pointer',
          fontSize: '0.75rem',
          fontWeight: 600,
          padding: '2px 8px',
          whiteSpace: 'nowrap',
          transition: 'background 0.15s',
          flexShrink: 0,
        }}
      >
        Dismiss
      </button>
    </div>
  )
}
