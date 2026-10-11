// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/BundleTimeline.tsx — the version rail (DESIGN-SYSTEM §3.3): the pipeline's
// versions (Bundles) as ticks on a time axis, newest on the right. A tick's glyph is its
// state; the followed version carries the thread's red, the compared one an ink dash,
// a rejected one is struck. Hover previews a version's route on the map (a ghost thread);
// click follows it; Shift-click (or c on a focused tick) compares it. [ and ] step to an
// older or newer version, f back to the newest. The 10 newest are shown, and the
// followed and compared versions always stay.
//
// Bundles are passed as a prop (managed by App) rather than fetched independently,
// avoiding duplicate requests and stale-state races (issue #321).
//
// #338: Shift-click selects a second version for comparison (the chip's tooltip says so).
// When two versions are selected, a "Compare" button appears.
// #532: Phase-driven visual properties use CSS classes (bundle-chip--{phase}).
import { useEffect, useRef } from 'react'
import type { Bundle } from '../types'
import { useRovingFocus } from '../useRovingFocus'
import { sortBundlesNewestFirst } from '../bundleSelection'
import { BundleTypeBadge, bundleTypeLabel } from './BundleTypeBadge'
import { displayState } from '../displayStates.gen'
import { versionName } from '../versionName'
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
  /** Open the comparison: of the given version (c on a tick), else of the picked one (Compare). */
  onCompare?: (bundleName?: string) => void
  /** Hover or focus on a tick: preview its route on the map; null when it ends. */
  onPreview?: (bundleName: string | null) => void
  /** The newest version (the pipeline's active Bundle): where f goes back to. */
  activeBundle?: string
  /** [ / ] / f anywhere on the page (not in a text field or a dialog). */
  globalKeys?: boolean
}

/** CSS class modifier for a given bundle phase; an empty phase maps to "unknown". */
export function phaseCSSClass(phase: string | undefined): string {
  return `bundle-chip--${(phase || 'unknown').toLowerCase()}`
}

/**
 * The ticks to show: the MAX_CHIPS newest bundles, plus the selected and
 * comparison bundles when they are older. Newest first.
 */
export function visibleBundles(bundles: Bundle[], keep: (string | undefined)[]): Bundle[] {
  const sorted = sortBundlesNewestFirst(bundles)
  return sorted.filter((b, i) => i < MAX_CHIPS || keep.includes(b.name))
}

/** The version one step older (-1) or newer (+1) than name, newest first in sorted. */
export function stepVersion(sorted: Bundle[], name: string | undefined, dir: -1 | 1): string | undefined {
  const i = sorted.findIndex(b => b.name === name)
  if (i < 0) return sorted[0]?.name
  return sorted[i - dir]?.name
}

/** Whether a key press belongs to a text field or an open dialog, not to the page. */
function typing(e: KeyboardEvent): boolean {
  const t = e.target as HTMLElement | null
  return !!t && (t instanceof HTMLInputElement || t instanceof HTMLTextAreaElement || t.isContentEditable ||
    !!document.querySelector('[aria-modal="true"]'))
}

export function BundleTimeline({ bundles, loading, onSelectBundle, selectedBundle, compareBundle, onCompareBundle, onCompare, onPreview, activeBundle, globalKeys }: Props) {
  const chips = useRef<HTMLDivElement>(null)
  const roving = useRovingFocus(chips, '.bundle-chip')

  // [ older, ] newer, f the newest: the followed version moves along the rail.
  useEffect(() => {
    if (!globalKeys) return
    const onKey = (e: KeyboardEvent) => {
      if (e.ctrlKey || e.metaKey || e.altKey || typing(e)) return
      const sorted = sortBundlesNewestFirst(bundles)
      const to = e.key === '[' ? stepVersion(sorted, selectedBundle, -1)
        : e.key === ']' ? stepVersion(sorted, selectedBundle, 1)
          : e.key === 'f' ? (activeBundle ?? sorted[0]?.name) : undefined
      if (!to || (e.key !== '[' && e.key !== ']' && e.key !== 'f')) return
      e.preventDefault()
      if (to !== selectedBundle) onSelectBundle?.(to)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [globalKeys, bundles, selectedBundle, activeBundle, onSelectBundle])

  // #784: skeleton loading state while bundles are being fetched
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

  // Oldest on the left, newest on the right: a time axis.
  const shown = visibleBundles(bundles, [selectedBundle, compareBundle]).reverse()
  if (shown.length === 0) return null

  const showCompare = compareBundle && selectedBundle && compareBundle !== selectedBundle
  const compareWith = (name: string) => {
    if (!onCompareBundle || name === selectedBundle) return
    onCompareBundle(name === compareBundle ? null : name)
  }

  return (
    <div className="bundle-timeline-bar">
      <div className="bundle-timeline__head">
        <span className="bundle-timeline__title">Versions (oldest → newest)</span>
        <span className="bundle-timeline__keys" aria-hidden="true"><kbd>[</kbd> <kbd>]</kbd> older / newer · <kbd>f</kbd> newest · <kbd>c</kbd> compare</span>
        {/* #338: Compare button appears when two bundles are selected */}
        {showCompare && (
          <button onClick={() => onCompare?.()} className="bundle-timeline__compare" title="Compare the two selected versions">Compare ↔</button>
        )}
        {compareBundle && (
          <button onClick={() => onCompareBundle?.(null)} className="bundle-timeline__clear" title="Clear comparison selection">× clear</button>
        )}
      </div>
      <div
        ref={chips}
        role="toolbar"
        aria-label="Versions (arrow keys move between versions; c compares the focused one)"
        onKeyDown={roving.onKeyDown}
        onFocus={roving.onFocus}
        onPointerLeave={() => onPreview?.(null)}
        className="bundle-timeline__chips"
      >
        <span className="bundle-timeline__axis" aria-hidden="true" />
        {shown.map(b => {
          const isSelected = b.name === selectedBundle
          const isCompare = b.name === compareBundle
          const phase = displayState('bundle', b.phase || 'Unknown')
          const chipClass = ['bundle-chip', phaseCSSClass(b.phase), isSelected ? 'bundle-chip--selected' : '', isCompare ? 'bundle-chip--compare' : ''].filter(Boolean).join(' ')
          return (
            <button
              key={b.name}
              onClick={(e) => { if (e.shiftKey) compareWith(b.name); else onSelectBundle?.(b.name) }}
              onKeyDown={(e) => {
                // c (or Shift+Enter) compares the focused version with the followed one, at once.
                if ((e.key === 'c' || (e.key === 'Enter' && e.shiftKey)) && !isSelected && onCompareBundle) {
                  e.preventDefault(); e.stopPropagation()
                  onCompareBundle(b.name)
                  onCompare?.(b.name)
                }
              }}
              onPointerEnter={() => { if (!isSelected) onPreview?.(b.name) }}
              onFocus={() => { if (!isSelected) onPreview?.(b.name) }}
              onBlur={() => onPreview?.(null)}
              title={`${b.name}: ${phase.label}${(b.type || 'image') === 'image' ? '' : `, ${bundleTypeLabel(b.type)} version`}${isCompare ? ' (comparison target)' : ''}${isSelected ? '' : '\nShift-click or c to compare'}`}
              aria-pressed={isSelected}
              data-roving-default={isSelected || undefined}
              className={chipClass}
              data-bundle-phase={b.phase}
              data-tone={phase.tone}
            >
              <span className="bundle-chip__dot" aria-hidden="true">{phase.glyph}</span>
              <span className="bundle-chip__name">{versionName(b.name, b.pipeline)}</span>
              <BundleTypeBadge type={b.type} compact />
              <span className={isSelected ? 'bundle-chip__phase' : 'bundle-chip__phase sr-only'}>{phase.label}</span>
              {isCompare && <span className="bundle-chip__compare-badge" aria-hidden="true">≠</span>}
            </button>
          )
        })}
      </div>
    </div>
  )
}
