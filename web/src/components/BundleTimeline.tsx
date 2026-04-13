// components/BundleTimeline.tsx — Horizontal timeline of bundle promotion history.
// Inspired by Kargo's freight timeline. Shows recent bundles as chips with
// per-environment state color coding. Newest bundles on the left.
//
// Bundles are passed as a prop (managed by App) rather than fetched independently,
// avoiding duplicate requests and stale-state races (issue #321).
//
// #338: Shift-click selects a second bundle for comparison.
// When two bundles are selected, a "Compare" button appears.
import type { Bundle } from '../types'

interface Props {
  /** Bundles for this pipeline — managed by the parent (App). */
  bundles: Bundle[]
  /** Callback when a bundle is selected — fetches its DAG. */
  onSelectBundle?: (bundleName: string) => void
  /** Currently selected bundle (highlighted). */
  selectedBundle?: string
  /** #338: Second bundle selected for comparison (shift-click). */
  compareBundle?: string
  /** Called when user shift-clicks a bundle to start comparison. */
  onCompareBundle?: (bundleName: string | null) => void
  /** Called when user clicks the Compare button. */
  onCompare?: () => void
}

/** Color for a bundle phase. */
function phaseColor(phase: string): string {
  switch (phase) {
    case 'Promoting': return '#6366f1'
    case 'Verified':  return '#22c55e'
    case 'Failed':    return '#ef4444'
    case 'Superseded': return '#475569'
    case 'Available': return '#f59e0b'
    default: return '#64748b'
  }
}

/** Short display name for a bundle (last 6 chars of suffix). */
function shortName(bundleName: string): string {
  const parts = bundleName.split('-')
  if (parts.length > 0) {
    const suffix = parts[parts.length - 1]
    return suffix.length >= 5 ? suffix : bundleName.slice(-6)
  }
  return bundleName.slice(-6)
}

export function BundleTimeline({ bundles, onSelectBundle, selectedBundle, compareBundle, onCompareBundle, onCompare }: Props) {
  // Sort newest-first by createdAt timestamp (ISO 8601), falling back to name (#337).
  // Name fallback ensures stability when createdAt is not yet populated.
  const sorted = [...bundles]
    .sort((a, b) => {
      if (a.createdAt && b.createdAt) {
        return a.createdAt > b.createdAt ? -1 : a.createdAt < b.createdAt ? 1 : 0
      }
      // Fallback: reverse-lexicographic name sort (newer bundles tend to have larger names)
      return a.name > b.name ? -1 : 1
    })
    .slice(0, 10)

  if (sorted.length === 0) return null

  const showCompare = compareBundle && selectedBundle && compareBundle !== selectedBundle

  return (
    <div style={{
      padding: '0.5rem 1rem',
      background: '#0f172a',
      borderBottom: '1px solid #1e293b',
      overflowX: 'auto',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.3rem' }}>
        <span style={{ fontSize: '0.65rem', color: '#475569', textTransform: 'uppercase', letterSpacing: '0.05em' }}>
          Bundle History (newest → oldest)
        </span>
        {/* #338: Compare button appears when two bundles are selected */}
        {showCompare && (
          <button
            onClick={onCompare}
            style={{
              fontSize: '0.65rem',
              background: '#1e1b4b',
              color: '#a5b4fc',
              border: '1px solid #4338ca',
              borderRadius: '3px',
              padding: '1px 6px',
              cursor: 'pointer',
              fontWeight: 600,
            }}
            title="Compare selected bundles"
          >
            Compare ↔
          </button>
        )}
        {compareBundle && (
          <button
            onClick={() => onCompareBundle?.(null)}
            style={{
              fontSize: '0.65rem',
              background: 'none',
              color: '#64748b',
              border: 'none',
              cursor: 'pointer',
              padding: '0',
            }}
            title="Clear comparison selection"
          >
            × clear
          </button>
        )}
        {!compareBundle && bundles.length >= 2 && (
          <span style={{ fontSize: '0.6rem', color: '#334155' }}>
            Shift-click to compare
          </span>
        )}
      </div>
      <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
        {sorted.map(b => {
          const isSelected = b.name === selectedBundle
          const isCompare = b.name === compareBundle
          const color = phaseColor(b.phase)
          return (
            <button
              key={b.name}
              onClick={(e) => {
                if (e.shiftKey) {
                  // #338: shift-click selects for comparison
                  if (onCompareBundle) onCompareBundle(isCompare ? null : b.name)
                } else {
                  onSelectBundle?.(b.name)
                }
              }}
              title={`${b.name}: ${b.phase}${isCompare ? ' (comparison target)' : ''}${'\nShift-click to compare'}`}
              style={{
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
                gap: '2px',
                padding: '0.3rem 0.5rem',
                background: isCompare ? '#1e1b4b' : isSelected ? '#1e293b' : 'transparent',
                border: `1px solid ${isCompare ? '#4338ca' : isSelected ? color : '#334155'}`,
                borderRadius: '4px',
                cursor: 'pointer',
                minWidth: '56px',
                outline: isCompare ? '1px solid #6366f1' : 'none',
              }}
            >
              {/* Phase dot */}
              <div style={{
                width: '8px', height: '8px', borderRadius: '50%',
                background: color,
                boxShadow: isSelected ? `0 0 6px ${color}` : 'none',
              }} />
              {/* Short name */}
              <span style={{
                fontSize: '0.75rem',
                color: isSelected || isCompare ? '#e2e8f0' : '#64748b',
                fontFamily: 'monospace',
                fontWeight: isSelected || isCompare ? 600 : 400,
              }}>
                {shortName(b.name)}
              </span>
              {/* Phase label */}
              <span style={{
                fontSize: '0.75rem',
                color,
              }}>
                {b.phase === 'Superseded' ? 'Sup' : b.phase}
              </span>
              {/* #338: "B" badge for comparison bundle */}
              {isCompare && (
                <span style={{
                  fontSize: '0.55rem',
                  color: '#a5b4fc',
                  fontWeight: 700,
                }}>⇅B</span>
              )}
            </button>
          )
        })}
      </div>
    </div>
  )
}
