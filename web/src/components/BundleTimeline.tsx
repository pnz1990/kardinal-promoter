// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/BundleTimeline.tsx — the pipeline's version history (versions are Bundles).
// Inspired by Kargo's freight timeline. Shows the 10 newest versions as chips,
// colored by phase, newest on the left. The selected version and the
// comparison version always stay visible, even when they are older than the 10th.
//
// Bundles are passed as a prop (managed by App) rather than fetched independently,
// avoiding duplicate requests and stale-state races (issue #321).
//
// #338: Shift-click selects a second version for comparison (the chip's tooltip says so).
// When two versions are selected, a "Compare" button appears.
// #532: Phase-driven visual properties use CSS classes (bundle-chip--{phase}).
import { useRef } from 'react'
import type { Bundle } from '../types'
import { useRovingFocus } from '../useRovingFocus'
import { sortBundlesNewestFirst } from '../bundleSelection'
import { BundleTypeBadge, bundleTypeLabel } from './BundleTypeBadge'
import { displayState } from '../displayStates.gen'
import { bundleVersion } from '../stage/stageModel'
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


export function BundleTimeline({ bundles, loading, onSelectBundle, selectedBundle, compareBundle, onCompareBundle, onCompare }: Props) {
  const chips = useRef<HTMLDivElement>(null)
  const roving = useRovingFocus(chips, '.bundle-chip')
  // #784: skeleton loading state — shimmer chips while bundles are being fetched
  if (loading) {
    return (
      <div className="bundle-timeline-bar" data-testid="bundle-timeline-skeleton">
        <span className="sr-only" role="status">Loading versions</span>
        <div className="bundle-timeline__skeleton-row">
          {[0, 1, 2, 3, 4].map(i => (
            <div key={i} aria-hidden="true" className="bundle-chip-skeleton" />
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
      <div className="bundle-timeline__head">
        <span className="bundle-timeline__title">
          Versions (newest → oldest)
        </span>
        {/* #338: Compare button appears when two bundles are selected */}
        {showCompare && (
          <button
            onClick={onCompare}
            className="bundle-timeline__compare"
            title="Compare the two selected versions"
          >
            Compare ↔
          </button>
        )}
        {compareBundle && (
          <button
            onClick={() => onCompareBundle?.(null)}
            className="bundle-timeline__clear"
            title="Clear comparison selection"
          >
            × clear
          </button>
        )}
      </div>
      <div
        ref={chips}
        role="toolbar"
        aria-label="Versions (arrow keys move between versions)"
        onKeyDown={roving.onKeyDown}
        onFocus={roving.onFocus}
        className="bundle-timeline__chips"
      >
        {shown.map(b => {
          const isSelected = b.name === selectedBundle
          const isCompare = b.name === compareBundle
          const phase = displayState('bundle', b.phase || 'Unknown')
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
              title={`${b.name}: ${phase.label}${(b.type || 'image') === 'image' ? '' : `, ${bundleTypeLabel(b.type)} version`}${isCompare ? ' (comparison target)' : ''}${isSelected ? '' : '\nShift-click to compare'}`}
              aria-pressed={isSelected}
              data-roving-default={isSelected || undefined}
              className={chipClass}
              data-bundle-phase={b.phase}
            >
              <span className="bundle-chip__dot" aria-hidden="true" />
              <span className="bundle-chip__name">{bundleVersion(b)}</span>
              <BundleTypeBadge type={b.type} compact />
              <span className="bundle-chip__phase">{phase.label}</span>
              {/* #338: "B" badge for comparison bundle */}
              {isCompare && <span className="bundle-chip__compare-badge">⇅B</span>}
            </button>
          )
        })}
      </div>
    </div>
  )
}
