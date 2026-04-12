// components/BlockedBanner.tsx — Banner shown when PolicyGates are blocking promotion.
// Inspired by kro-ui's compile-error banner pattern.

interface Props {
  blockedCount: number
  onShowBlocked: () => void
  showingBlocked: boolean
}

export function BlockedBanner({ blockedCount, onShowBlocked, showingBlocked }: Props) {
  if (blockedCount === 0) return null

  return (
    <div
      role="alert"
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: '0.75rem',
        padding: '0.6rem 1rem',
        marginBottom: '0.75rem',
        background: '#7f1d1d',
        borderRadius: '6px',
        border: '1px solid #b91c1c',
        fontSize: '0.85rem',
        color: '#fca5a5',
      }}
    >
      <span style={{ fontSize: '1rem' }} aria-hidden="true">⚠</span>
      <span style={{ flex: 1 }}>
        <strong style={{ color: '#fecaca' }}>
          {blockedCount} PolicyGate{blockedCount > 1 ? 's' : ''} blocking prod
        </strong>
      </span>
      <button
        onClick={onShowBlocked}
        style={{
          background: showingBlocked ? '#b91c1c' : 'transparent',
          border: '1px solid #b91c1c',
          borderRadius: '4px',
          color: '#fca5a5',
          cursor: 'pointer',
          fontSize: '0.78rem',
          padding: '0.25rem 0.6rem',
          fontWeight: 600,
          whiteSpace: 'nowrap',
        }}
        aria-pressed={showingBlocked}
      >
        {showingBlocked ? 'Clear filter' : 'Show blocked'}
      </button>
    </div>
  )
}
