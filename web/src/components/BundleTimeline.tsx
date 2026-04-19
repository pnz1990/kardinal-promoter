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
  /** #784: Show skeleton loading state instead of bundles while fetching. */
  loading?: boolean
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
// Bundle chip backgrounds are always hardcoded dark (#0f172a, #1e1b4b).
// Phase accent colors must always be readable on dark — use hardcoded dark-mode values
// rather than CSS variables that would flip to dark in light mode.
function phaseAccentColor(phase: string): string {
  switch (phase) {
    case 'Promoting': return '#a5b4fc'   // indigo-300: 9.5:1 on #0f172a ✓ (was var(--color-accent) = #4f46e5 in light)
    case 'Verified':  return '#4ade80'   // green-400: 8.8:1 on #0f172a ✓ (was var(--color-success))
    case 'Failed':    return '#ef4444'   // red-500 — unchanged (works on dark bg)
    case 'Superseded': return '#94a3b8'  // slate-400: 7.1:1 on #0f172a ✓ (was var(--color-text-faint) = #4b5563 in light)
    case 'Available': return '#f59e0b'   // amber-500 — unchanged (works on dark bg)
    default: return '#94a3b8'            // slate-400: 7.1:1 on #0f172a ✓ (was #64748b = 4.1:1 fail)
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

export function BundleTimeline({ bundles, loading, onSelectBundle, selectedBundle, compareBundle, onCompareBundle, onCompare }: Props) {
  // #784: skeleton loading state — shimmer chips while bundles are being fetched
  if (loading) {
    return (
      <div style={{
        padding: '0.5rem 1rem',
        background: 'var(--color-bg)',
        borderBottom: '1px solid #1e293b',
        overflowX: 'auto',
      }} data-testid="bundle-timeline-skeleton">
        <style>{`
          @keyframes shimmer-bt {
            0% { background-position: 200% 0; }
            100% { background-position: -200% 0; }
          }
        `}</style>
        <span className="sr-only" role="status" style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0,0,0,0)', whiteSpace: 'nowrap' }}>
          Loading bundles
        </span>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          {[72, 58, 64, 50, 68].map((w, i) => (
            <div
              key={i}
              aria-hidden="true"
              style={{
                height: '28px',
                borderRadius: '14px',
                background: 'linear-gradient(90deg, #1e293b 25%, #293548 50%, #1e293b 75%)',
                backgroundSize: '200% 100%',
                animation: 'shimmer-bt 1.5s infinite',
                width: `${w}px`,
                flexShrink: 0,
              }}
            />
          ))}
        </div>
      </div>
    )
  }

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
          // color-text-muted (7.05:1 on dark bg, 5.74:1 on light bg) — WCAG AA ✓
          // color-border fails in light mode (#cbd5e1 on #f1f5f9 = 1.3:1)
          <span style={{ fontSize: '0.6rem', color: 'var(--color-text-muted)' }}>
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
                // Bundle chip backgrounds are always dark (#1e1b4b selected, #0f172a default).
                // Use hardcoded light text for selected/compare — var(--color-text) flips to
                // dark in light mode and fails contrast against the dark chip bg (#1e1b4b).
                // #e2e8f0 (slate-200) = 13.6:1 on #1e1b4b ✓
                // #94a3b8 (slate-400) = 7.1:1 on #0f172a ✓ (was #64748b = 4.1:1 fail)
                color: isSelected || isCompare ? '#e2e8f0' : '#94a3b8',
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
