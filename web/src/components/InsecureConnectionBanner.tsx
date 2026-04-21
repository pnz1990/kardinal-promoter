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

/**
 * Returns true when the page is accessed over plain HTTP from a non-localhost origin.
 * Port-forward to localhost (http://localhost:* or http://127.0.0.1:*) is safe and
 * is the documented access method — it must NOT trigger the warning.
 */
export function isInsecureNonLocalConnection(): boolean {
  if (typeof window === 'undefined') return false
  if (window.location.protocol === 'https:') return false
  const host = window.location.hostname
  if (host === 'localhost' || host === '127.0.0.1' || host === '::1') return false
  return true
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
        background: '#451a03',
        border: '1px solid #92400e',
        borderRadius: '6px',
        padding: '0.5rem 0.75rem',
        marginBottom: '0.75rem',
        gap: '0.75rem',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
        <span style={{ color: 'var(--color-warning)', fontSize: '0.9rem' }} aria-hidden="true">⚠</span>
        <span style={{ fontSize: '0.82rem', color: '#fcd34d', fontWeight: 600 }}>
          Insecure connection — kardinal UI is accessed over plain HTTP.{' '}
          Use{' '}
          <code style={{ fontFamily: 'monospace', fontSize: '0.78rem' }}>
            kubectl port-forward svc/kardinal-controller 8082
          </code>{' '}
          and open{' '}
          <a href="http://localhost:8082/ui/" style={{ color: '#fde68a' }}>
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
          border: '1px solid #92400e',
          borderRadius: '4px',
          color: '#fcd34d',
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
