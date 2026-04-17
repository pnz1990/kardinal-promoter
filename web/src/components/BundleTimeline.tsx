// components/BundleTimeline.tsx — Horizontal timeline of bundle promotion history.
// Inspired by Kargo's freight timeline. Shows recent bundles as chips with
// per-environment state color coding. Newest bundles on the left.
//
// Bundles are passed as a prop (managed by App) rather than fetched independently,
// avoiding duplicate requests and stale-state races (issue #321).
//
// #338: Shift-click selects a second bundle for comparison.
// When two bundles are selected, a "Compare" button appears.
// #532: Phase-driven visual properties use CSS classes (bundle-chip--{phase}).
import type { Bundle } from '../types'
import '../styles/BundleTimeline.css'

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

/** CSS class modifier for a given bundle phase. */
function phaseCSSClass(phase: string): string {
  return `bundle-chip--${phase.toLowerCase()}`
}

/** Accent color for glow/dot — kept for inline uses (boxShadow, dot fill). */
function phaseAccentColor(phase: string): string {
  switch (phase) {
    case 'Promoting': return 'var(--color-accent)'
    case 'Verified':  return 'var(--color-success)'
    case 'Failed':    return '#ef4444'
    case 'Superseded': return 'var(--color-text-faint)'
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
      background: 'var(--color-bg)',
      borderBottom: '1px solid #1e293b',
      overflowX: 'auto',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.3rem' }}>
        <span style={{ fontSize: '0.65rem', color: 'var(--color-text-faint)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>
          Bundle History (newest → oldest)
        </span>
        {/* #338: Compare button appears when two bundles are selected */}
        {showCompare && (
          <button
            onClick={onCompare}
            style={{
              fontSize: '0.65rem',
              background: '#1e1b4b',
              color: 'var(--color-accent)',
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
          <span style={{ fontSize: '0.6rem', color: 'var(--color-border)' }}>
            Shift-click to compare
          </span>
        )}
      </div>
      <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
        {sorted.map(b => {
          const isSelected = b.name === selectedBundle
          const isCompare = b.name === compareBundle
          const accentColor = phaseAccentColor(b.phase)
          // #532: CSS classes for state-driven styling
          const chipClass = [
            'bundle-chip',
            phaseCSSClass(b.phase),
            isSelected ? 'bundle-chip--selected' : '',
            isCompare ? 'bundle-chip--compare' : '',
          ].filter(Boolean).join(' ')

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
              className={chipClass}
              data-bundle-phase={b.phase}
              style={{
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
                gap: '2px',
                minWidth: '56px',
                outline: isCompare ? '1px solid #6366f1' : 'none',
              }}
            >
              {/* Phase dot */}
              <div style={{
                width: '8px', height: '8px', borderRadius: '50%',
                background: accentColor,
                boxShadow: isSelected ? `0 0 6px ${accentColor}` : 'none',
              }} />
              {/* Short name */}
              <span style={{
                fontSize: '0.75rem',
                color: isSelected || isCompare ? 'var(--color-text)' : '#64748b',
                fontFamily: 'monospace',
                fontWeight: isSelected || isCompare ? 600 : 400,
              }}>
                {shortName(b.name)}
              </span>
              {/* Phase label */}
              <span style={{
                fontSize: '0.75rem',
                color: accentColor,
              }}>
                {b.phase === 'Superseded' ? 'Sup' : b.phase}
              </span>
              {/* #338: "B" badge for comparison bundle */}
              {isCompare && (
                <span style={{
                  fontSize: '0.55rem',
                  color: 'var(--color-accent)',
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
