// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/BundleTimeline.tsx — Horizontal timeline of bundle promotion history.
// Inspired by Kargo's freight timeline. Shows the 10 newest bundles as chips,
// colored by bundle phase, newest on the left. The selected bundle and the
// comparison bundle always stay visible, even when they are older than the 10th.
//
// Bundles are passed as a prop (managed by App) rather than fetched independently,
// avoiding duplicate requests and stale-state races (issue #321).
//
// #338: Shift-click selects a second bundle for comparison.
// When two bundles are selected, a "Compare" button appears.
// #532: Phase-driven visual properties use CSS classes (bundle-chip--{phase}).
import type { Bundle } from '../types'
import { sortBundlesNewestFirst } from '../bundleSelection'
import '../styles/BundleTimeline.css'

/** Number of newest bundles shown. */
const MAX_CHIPS = 10

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

/** CSS class modifier for a given bundle phase; an empty phase maps to "unknown". */
export function phaseCSSClass(phase: string | undefined): string {
  return `bundle-chip--${(phase || 'unknown').toLowerCase()}`
}

/**
 * The chips to show: the MAX_CHIPS newest bundles, plus the selected and
 * comparison bundles when they are older. Newest first.
 */
export function visibleBundles(bundles: Bundle[], keep: (string | undefined)[]): Bundle[] {
  const sorted = sortBundlesNewestFirst(bundles)
  return sorted.filter((b, i) => i < MAX_CHIPS || keep.includes(b.name))
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
      <div className="bundle-timeline-bar" data-testid="bundle-timeline-skeleton">
        <span className="sr-only" role="status">Loading bundles</span>
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          {[72, 58, 64, 50, 68].map((w, i) => (
            <div key={i} aria-hidden="true" className="bundle-chip-skeleton" style={{ width: `${w}px` }} />
          ))}
        </div>
      </div>
    )
  }

  const shown = visibleBundles(bundles, [selectedBundle, compareBundle])
  if (shown.length === 0) return null

  const showCompare = compareBundle && selectedBundle && compareBundle !== selectedBundle

  return (
    <div className="bundle-timeline-bar">
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
              background: 'var(--color-accent-bg)',
              color: 'var(--color-accent)',
              border: '1px solid var(--color-accent)',
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
              color: 'var(--color-text-muted)',
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
          <span style={{ fontSize: '0.6rem', color: 'var(--color-text-muted)' }}>
            Shift-click to compare
          </span>
        )}
      </div>
      <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
        {shown.map(b => {
          const isSelected = b.name === selectedBundle
          const isCompare = b.name === compareBundle
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
                  // #338: shift-click selects for comparison. The selected
                  // bundle is the one compared against, so it is ignored.
                  if (onCompareBundle && !isSelected) onCompareBundle(isCompare ? null : b.name)
                } else {
                  onSelectBundle?.(b.name)
                }
              }}
              title={`${b.name}: ${b.phase || 'Unknown'}${isCompare ? ' (comparison target)' : ''}${isSelected ? '' : '\nShift-click to compare'}`}
              aria-pressed={isSelected}
              className={chipClass}
              data-bundle-phase={b.phase}
            >
              <span className="bundle-chip__dot" aria-hidden="true" />
              <span className="bundle-chip__name">{shortName(b.name)}</span>
              <span className="bundle-chip__phase">
                {b.phase === 'Superseded' ? 'Sup' : (b.phase || 'Unknown')}
              </span>
              {/* #338: "B" badge for comparison bundle */}
              {isCompare && <span className="bundle-chip__compare-badge">⇅B</span>}
            </button>
          )
        })}
      </div>
    </div>
  )
}
