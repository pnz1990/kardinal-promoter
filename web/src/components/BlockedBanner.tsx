// components/BlockedBanner.tsx — Banner shown when PolicyGates are blocking promotion.
//
// Adapted from kro-ui's AnomalyBanner (compile-error banner pattern).
// Shows count of blocked PolicyGates with a "Show blocked" button to highlight them.

interface BlockedBannerProps {
  /** Number of blocked PolicyGate nodes. */
  blockedCount: number
  /** Whether the highlight filter is currently active. */
  highlightActive: boolean
  /** Toggle the highlight filter. */
  onToggleHighlight: () => void
}

/**
 * BlockedBanner renders an amber warning banner when one or more PolicyGates
 * are in a blocked state. It includes a toggle to highlight the blocked nodes in the DAG.
 */
export function BlockedBanner({ blockedCount, highlightActive, onToggleHighlight }: BlockedBannerProps) {
  if (blockedCount === 0) return null

  const label = blockedCount === 1
    ? '1 PolicyGate blocking promotion'
    : `${blockedCount} PolicyGates blocking promotion`

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
          {label}
        </span>
      </div>
      <button
        onClick={onToggleHighlight}
        aria-pressed={highlightActive}
        style={{
          background: highlightActive ? '#92400e' : 'none',
          border: '1px solid #92400e',
          borderRadius: '4px',
          color: '#fcd34d',
          cursor: 'pointer',
          fontSize: '0.75rem',
          fontWeight: 600,
          padding: '2px 8px',
          whiteSpace: 'nowrap',
          transition: 'background 0.15s',
        }}
      >
        {highlightActive ? 'Show all' : 'Show blocked'}
      </button>
    </div>
  )
}
