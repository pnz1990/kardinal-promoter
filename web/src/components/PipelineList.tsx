// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/PipelineList.tsx — Sidebar list of Pipelines with health chips,
// bundle name, environment count, and namespace indicator.
// With no pipelines it shows one line; the onboarding card with the setup
// commands is the main panel's EmptyState, so there is one set of instructions.
// #345: debounced search/filter input at the top.
// #800: searchInputRef prop exposes the filter input for the / keyboard shortcut.
// #815: virtual scrolling for flat lists with >50 entries (@tanstack/react-virtual).
import { useState, useCallback, useRef, type RefObject } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import type { Pipeline } from '../types'
import { HealthChip } from './HealthChip'
import CopyButton from './CopyButton'
import '../styles/PipelineList.css'

/** Number of pipelines above which virtual scrolling is enabled (flat list only). */
const VIRTUAL_THRESHOLD = 50

interface Props {
  pipelines: Pipeline[]
  /** Name of the selected pipeline. */
  selected?: string
  /** Namespace of the selected pipeline. Pipelines are identified by
   *  namespace + name, so two same-named pipelines are never both selected. */
  selectedNamespace?: string
  onSelect: (name: string, namespace: string) => void
  loading?: boolean
  error?: string
  /**
   * #800: Ref forwarded to the filter <input> so App can call
   * searchInputRef.current?.focus() when the / shortcut fires.
   */
  searchInputRef?: RefObject<HTMLInputElement | null>
  /**
   * Pipelines in the cluster before the fleet health bar's filter (default:
   * pipelines.length). When that filter hides every pipeline, the list keeps
   * its filter input and says nothing matches, instead of the note for an
   * empty cluster.
   */
  total?: number
}

/** Shorten a long bundle name for the sidebar: names over 14 characters keep
 *  their first 12 and end in an ellipsis; the full name is in the row's title. */
function shortBundleName(name: string | undefined): string | null {
  if (!name) return null
  if (name.length <= 14) return name
  return name.slice(0, 12) + '…'
}

/** Sidebar note for an empty cluster. */
function NoPipelines() {
  return (
    <p className="pipeline-list__note">
      No pipelines found.
    </p>
  )
}

/** True when p matches the query by name, namespace, or namespace/name. */
function matchesQuery(p: Pipeline, query: string): boolean {
  return p.name.toLowerCase().includes(query) ||
    p.namespace.toLowerCase().includes(query) ||
    `${p.namespace}/${p.name}`.toLowerCase().includes(query)
}

export function PipelineList({ pipelines, selected, selectedNamespace, onSelect, loading, error, searchInputRef, total }: Props) {
  const isSelected = (p: Pipeline) =>
    selected === p.name && (selectedNamespace === undefined || selectedNamespace === p.namespace)

  // #345: search/filter state with debounce
  const [searchQuery, setSearchQuery] = useState('')
  const debounceTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [debouncedQuery, setDebouncedQuery] = useState('')

  // #815: container ref for virtual scrolling
  const listContainerRef = useRef<HTMLDivElement>(null)

  const handleSearchChange = useCallback((value: string) => {
    setSearchQuery(value)
    if (debounceTimer.current) clearTimeout(debounceTimer.current)
    debounceTimer.current = setTimeout(() => {
      setDebouncedQuery(value.trim().toLowerCase())
    }, 150)
  }, [])

  // #345: filter pipelines by search query (includes namespace prefix search).
  // #815: computed BEFORE early returns so useVirtualizer is always called
  // with the same hook order (Rules of Hooks).
  const filteredPipelines = debouncedQuery
    ? pipelines.filter(p => matchesQuery(p, debouncedQuery))
    : pipelines

  // #358: detect multi-namespace setup — group by namespace when needed.
  const uniqueNamespaces = new Set(pipelines.map(p => p.namespace))
  const isMultiNamespace = uniqueNamespaces.size > 1
  const useVirtual = !isMultiNamespace && filteredPipelines.length > VIRTUAL_THRESHOLD

  // #815: Hook must be called unconditionally — before any early returns.
  const virtualizer = useVirtualizer({
    count: filteredPipelines.length,
    getScrollElement: () => listContainerRef.current,
    estimateSize: () => 52,
    overscan: 5,
  })

  if (loading) {
    // #335: skeleton loading state — shimmer placeholders instead of "Loading pipelines..."
    return (
      <div className="pipeline-list__skeleton">
        {[0, 1, 2, 3].map(i => (
          <div key={i} className="pipeline-list__skeleton-row" />
        ))}
      </div>
    )
  }
  // A failed read before any worked: there is no list to keep.
  if (error && (total ?? pipelines.length) === 0) {
    return (
      <div className="pipeline-list__load-error">
        Error: {error}
      </div>
    )
  }
  if ((total ?? pipelines.length) === 0) {
    return <NoPipelines />
  }

  // Group by namespace for multi-namespace display
  const pipelinesByNamespace: Record<string, typeof filteredPipelines> = {}
  for (const p of filteredPipelines) {
    if (!pipelinesByNamespace[p.namespace]) pipelinesByNamespace[p.namespace] = []
    pipelinesByNamespace[p.namespace].push(p)
  }
  const namespaceOrder = Array.from(uniqueNamespaces).sort()

  return (
    <div>
      {/* #345 #800: search/filter input — always rendered so / shortcut always works.
          Esc clears the filter and blurs the input (O3 of spec issue-800). */}
      <div className="pipeline-list__search">
        <input
          ref={searchInputRef}
          type="text"
          placeholder={isMultiNamespace ? "Filter by name or namespace…" : "Filter pipelines…"}
          value={searchQuery}
          onChange={e => handleSearchChange(e.target.value)}
          onKeyDown={e => {
            // #800: Esc in filter clears and blurs — distinct from global Esc (close panel).
            if (e.key === 'Escape') {
              e.stopPropagation() // prevent global Esc from also firing
              handleSearchChange('')
              ;(e.target as HTMLInputElement).blur()
            }
          }}
          aria-label="Filter pipelines by name or namespace"
          className="pipeline-list__filter"
        />
        {searchQuery && (
          <button
            onClick={() => handleSearchChange('')}
            aria-label="Clear filter"
            className="pipeline-list__clear"
          >×</button>
        )}
      </div>
      {/* A failed poll keeps the last list App read; the error shows above it
          until a poll works again. */}
      {error && (
        <div
          role="alert"
          className="pipeline-list__error"
        >
          Error: {error}
        </div>
      )}
      {/* #815: List container — scrollable, used as virtual scroll container when active */}
      <div
        ref={listContainerRef}
        className={useVirtual ? 'pipeline-list__scroll' : undefined}
      >
        {filteredPipelines.length === 0 && (
          <div className="pipeline-list__no-match">
            {debouncedQuery
              ? <>No pipelines match &ldquo;{debouncedQuery}&rdquo;</>
              : 'No pipelines match this filter.'}
          </div>
        )}
        {/* #358: multi-namespace grouped display — not virtualized (variable header heights) */}
        {isMultiNamespace ? (
          <ul role="list" aria-label="Pipelines" className="pipeline-list__list">
            {namespaceOrder.map(ns => {
              const nsPipelines = pipelinesByNamespace[ns]
              if (!nsPipelines || nsPipelines.length === 0) return null
              return (
                <li key={ns}>
                  {/* Namespace header */}
                  <div className="pipeline-list__namespace">
                    {ns}
                  </div>
                  <ul role="group" className="pipeline-list__list">
                    {nsPipelines.map(p => renderPipelineItem(p))}
                  </ul>
                </li>
              )
            })}
          </ul>
        ) : useVirtual ? (
          /* #815: virtual scrolling for flat lists > VIRTUAL_THRESHOLD */
          <ul
            role="list"
            aria-label="Pipelines"
            className="pipeline-list__list pipeline-list__list--virtual"
            style={{ height: `${virtualizer.getTotalSize()}px` }}
          >
            {virtualizer.getVirtualItems().map(virtualItem => {
              const p = filteredPipelines[virtualItem.index]
              if (!p) return null
              return (
                <li
                  key={virtualItem.key}
                  className="pipeline-list__item pipeline-list__item--virtual"
                  style={{ transform: `translateY(${virtualItem.start}px)` }}
                  data-index={virtualItem.index}
                  ref={virtualizer.measureElement}
                >
                  {renderPipelineItemContent(p)}
                </li>
              )
            })}
          </ul>
        ) : (
          /* Normal flat rendering for ≤ VIRTUAL_THRESHOLD pipelines */
          <ul role="list" aria-label="Pipelines" className="pipeline-list__list">
            {filteredPipelines.map(p => renderPipelineItem(p))}
          </ul>
        )}
      </div>
    </div>
  )

  // #358: renderPipelineItem as inner function for reuse in grouped and flat display
  // #815: renderPipelineItemContent returns just the inner row content (no <li> wrapper)
  //       so the virtual scrolling path can provide its own <li> with positioning.
  function renderPipelineItemContent(p: Pipeline) {
        const bundle = shortBundleName(p.activeBundleName)
        const envCount = p.environmentCount
        const selectedRow = isSelected(p)

        return (
          // #762: Outer flex container. Selection <button> + CopyButton are siblings
          // (not nested) to satisfy the axe nested-interactive rule.
          <>
          {/* A native button already turns Enter and Space into one click (#C10b-web-22). */}
          <button
            onClick={() => onSelect(p.name, p.namespace)}
            aria-pressed={selectedRow}
            className="pipeline-list__row"
          >
            {/* Pipeline name + phase badge */}
            <div className={`pipeline-list__head${bundle || envCount ? ' pipeline-list__head--sub' : ''}`}>
              <div className="pipeline-list__name-wrap">
                <span className="pipeline-list__name">
                  {p.name}
                </span>
              </div>
              <div className="pipeline-list__badges">
                {/* Paused badge — visible accent when pipeline is paused (#328) */}
                {p.paused && (
                  <span
                    title="Pipeline is paused — no new promotions will start"
                    className="pipeline-list__paused"
                  >
                    PAUSED
                  </span>
                )}
                {p.phase && (
                  // #523: phase "Unknown" means no bundle yet — show "Idle".
                  // E2E-R05: a promoting pipeline held by a PolicyGate reads
                  // "Blocked" (amber, like the fleet bar's Blocked badge).
                  <HealthChip
                    state={p.paused ? 'Paused' : p.phase}
                    label={p.paused ? undefined
                      : p.phase === 'Unknown' ? 'Idle'
                      : p.phase === 'Promoting' && (p.blockerCount ?? 0) > 0 ? 'Blocked'
                      : undefined}
                    size="sm"
                  />
                )}
              </div>
            </div>

             {/* Sub-line: env count + health bar + active bundle (#342) */}
            {(bundle || envCount > 0) && (
              <div className="pipeline-list__sub">
                {/* Multi-segment health bar when env states are available (#342) */}
                {p.environmentStates && Object.keys(p.environmentStates).length > 0 ? (
                  <div className="pipeline-list__states">
                    <span>{envCount} env{envCount !== 1 ? 's' : ''}</span>
                    <span className="pipeline-list__dot">·</span>
                    {/* State badges: count per phase */}
                    {(() => {
                      const counts: Record<string, number> = {}
                      for (const phase of Object.values(p.environmentStates!)) {
                        counts[phase] = (counts[phase] ?? 0) + 1
                      }
                      const phaseTone: Record<string, string> = {
                        Verified: 'verified', Promoting: 'promoting', WaitingForMerge: 'promoting',
                        HealthChecking: 'waiting', Verifying: 'waiting', Failed: 'failed', Pending: 'pending',
                      }
                      return Object.entries(counts).map(([phase, count]) => (
                        <span key={phase} className="pipeline-list__state" data-tone={phaseTone[phase]} title={`${count} env${count !== 1 ? 's' : ''} in ${phase}`}>
                          {count} {phase === 'WaitingForMerge' ? 'PR' : phase === 'HealthChecking' ? 'health' : phase.toLowerCase()}
                        </span>
                      ))
                    })()}
                  </div>
                ) : (
                  <div className="pipeline-list__meta">
                    {envCount > 0 && (
                      <span>{envCount} env{envCount !== 1 ? 's' : ''}</span>
                    )}
                    {bundle && (
                      <>
                        {envCount > 0 && <span>·</span>}
                        <span
                          className="pipeline-list__bundle"
                          title={p.activeBundleName}
                        >
                          {bundle}
                        </span>
                      </>
                    )}
                  </div>
                )}
                {/* Bundle name shown below the health bar when env states are shown */}
                {p.environmentStates && bundle && (
                  <span className="pipeline-list__bundle" title={p.activeBundleName}>
                    {bundle}
                  </span>
                )}
              </div>
            )}
          </button>
          {/* #763: CopyButton as a sibling of the selection button — not nested inside it.
              Flex sibling renders as a narrow strip on the right of the row. */}
          <div className="pipeline-list__copy" data-selected={selectedRow || undefined}>
            <CopyButton text={p.name} title={`Copy pipeline name "${p.name}"`} />
          </div>
          </>
        )
  }

  /** renderPipelineItem wraps content in an <li> for normal (non-virtual) rendering. */
  function renderPipelineItem(p: Pipeline) {
    return (
      <li
        key={`${p.namespace}/${p.name}`}
        className="pipeline-list__item"
      >
        {renderPipelineItemContent(p)}
      </li>
    )
  }
}
