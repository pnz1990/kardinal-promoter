// components/BundleTimeline.tsx — Horizontal timeline of bundle promotion history.
// Inspired by Kargo's freight timeline. Shows recent bundles as chips with
// per-environment state color coding. Newest bundles on the left.
//
// Bundles are passed as a prop (managed by App) rather than fetched independently,
// avoiding duplicate requests and stale-state races (issue #321).
import type { Bundle } from '../types'

interface Props {
  /** Bundles for this pipeline — managed by the parent (App). */
  bundles: Bundle[]
  /** Callback when a bundle is selected — fetches its DAG. */
  onSelectBundle?: (bundleName: string) => void
  /** Currently selected bundle (highlighted). */
  selectedBundle?: string
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

export function BundleTimeline({ bundles, onSelectBundle, selectedBundle }: Props) {
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

  return (
    <div style={{
      padding: '0.5rem 1rem',
      background: '#0f172a',
      borderBottom: '1px solid #1e293b',
      overflowX: 'auto',
    }}>
      <div style={{ fontSize: '0.65rem', color: '#475569', marginBottom: '0.3rem', textTransform: 'uppercase', letterSpacing: '0.05em' }}>
        Bundle History (newest → oldest)
      </div>
      <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
        {sorted.map(b => {
          const isSelected = b.name === selectedBundle
          const color = phaseColor(b.phase)
          return (
            <button
              key={b.name}
              onClick={() => onSelectBundle?.(b.name)}
              title={`${b.name}: ${b.phase}`}
              style={{
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
                gap: '2px',
                padding: '0.3rem 0.5rem',
                background: isSelected ? '#1e293b' : 'transparent',
                border: `1px solid ${isSelected ? color : '#334155'}`,
                borderRadius: '4px',
                cursor: 'pointer',
                minWidth: '56px',
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
                color: isSelected ? '#e2e8f0' : '#64748b',
                fontFamily: 'monospace',
                fontWeight: isSelected ? 600 : 400,
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
            </button>
          )
        })}
      </div>
    </div>
  )
}
