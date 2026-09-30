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
    <p style={{ padding: '1rem', margin: 0, color: 'var(--color-text-muted)', fontSize: '0.8rem' }}>
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

export function PipelineList({ pipelines, selected, selectedNamespace, onSelect, loading, error, searchInputRef }: Props) {
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
      <div style={{ padding: '0.5rem 0' }}>
        <style>{`
          @keyframes shimmer-pl {
            0% { background-position: 200% 0; }
            100% { background-position: -200% 0; }
          }
        `}</style>
        {[80, 65, 90, 70].map((w, i) => (
          <div
            key={i}
            style={{
              height: '42px',
              borderRadius: '4px',
              background: 'linear-gradient(90deg, var(--color-surface) 25%, var(--color-surface-2) 50%, var(--color-surface) 75%)',
              backgroundSize: '200% 100%',
              animation: 'shimmer-pl 1.5s infinite',
              margin: '0.3rem 1rem',
              width: `${w}%`,
            }}
          />
        ))}
      </div>
    )
  }
  if (error) {
    return (
      <div style={{ padding: '1rem', color: 'var(--color-error)', fontSize: '0.82rem' }}>
        Error: {error}
      </div>
    )
  }
  if (pipelines.length === 0) {
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
      <div style={{ padding: '0.5rem 1rem 0.25rem', position: 'relative' }}>
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
          style={{
            width: '100%',
            boxSizing: 'border-box',
            background: 'var(--color-surface)',
            border: '1px solid var(--color-border)',
            borderRadius: '4px',
            padding: '0.3rem 1.75rem 0.3rem 0.5rem',
            fontSize: '0.78rem',
            color: 'var(--color-text)',
            outline: 'none',
          }}
        />
        {searchQuery && (
          <button
            onClick={() => handleSearchChange('')}
            aria-label="Clear filter"
            style={{
              position: 'absolute',
              right: '1.3rem',
              top: '50%',
              transform: 'translateY(-50%)',
              background: 'none',
              border: 'none',
              cursor: 'pointer',
              color: 'var(--color-text-muted)',
              fontSize: '0.9rem',
              padding: '0 2px',
              lineHeight: 1,
            }}
          >×</button>
        )}
      </div>
      {/* #815: List container — scrollable, used as virtual scroll container when active */}
      <div
        ref={listContainerRef}
        style={useVirtual ? { overflowY: 'auto', maxHeight: '100%' } : undefined}
      >
        {filteredPipelines.length === 0 && debouncedQuery && (
          <div style={{ padding: '0.75rem 1rem', color: 'var(--color-text-muted)', fontSize: '0.8rem' }}>
            No pipelines match &ldquo;{debouncedQuery}&rdquo;
          </div>
        )}
        {/* #358: multi-namespace grouped display — not virtualized (variable header heights) */}
        {isMultiNamespace ? (
          <ul role="list" aria-label="Pipelines" style={{ listStyle: 'none', padding: 0, margin: 0 }}>
            {namespaceOrder.map(ns => {
              const nsPipelines = pipelinesByNamespace[ns]
              if (!nsPipelines || nsPipelines.length === 0) return null
              return (
                <li key={ns}>
                  {/* Namespace header */}
                  <div style={{
                    padding: '0.3rem 1rem 0.15rem',
                    fontSize: '0.65rem',
                    color: 'var(--color-text-faint)',
                    textTransform: 'uppercase',
                    letterSpacing: '0.05em',
                    borderTop: '1px solid var(--color-border-muted)',
                    fontFamily: 'monospace',
                    background: 'var(--color-bg-deep)',
                  }}>
                    {ns}
                  </div>
                  <ul role="group" style={{ listStyle: 'none', padding: 0, margin: 0 }}>
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
            style={{
              listStyle: 'none',
              padding: 0,
              margin: 0,
              height: `${virtualizer.getTotalSize()}px`,
              position: 'relative',
            }}
          >
            {virtualizer.getVirtualItems().map(virtualItem => {
              const p = filteredPipelines[virtualItem.index]
              if (!p) return null
              return (
                <li
                  key={virtualItem.key}
                  style={{
                    position: 'absolute',
                    top: 0,
                    left: 0,
                    width: '100%',
                    transform: `translateY(${virtualItem.start}px)`,
                    listStyle: 'none',
                    display: 'flex',
                    alignItems: 'stretch',
                  }}
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
          <ul role="list" aria-label="Pipelines" style={{ listStyle: 'none', padding: 0, margin: 0 }}>
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
            style={{
              flex: 1,
              textAlign: 'left',
              padding: '0.6rem 1rem',
              cursor: 'pointer',
              background: selectedRow ? 'var(--color-surface)' : 'transparent',
              borderLeft: selectedRow ? '3px solid var(--color-accent)' : '3px solid transparent',
              borderTop: 'none',
              borderRight: 'none',
              borderBottom: 'none',
              minWidth: 0,
            }}
          >
            {/* Pipeline name + phase badge */}
            <div style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              marginBottom: bundle || envCount ? '0.2rem' : 0,
            }}>
              <div style={{ display: 'flex', alignItems: 'center', minWidth: 0 }}>
                <span style={{
                  fontWeight: selectedRow ? 600 : 400,
                  fontSize: '0.85rem',
                  color: 'var(--color-text)',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                  maxWidth: '105px',
                }}>
                  {p.name}
                </span>
              </div>
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.25rem' }}>
                {/* Paused badge — visible accent when pipeline is paused (#328) */}
                {p.paused && (
                  <span
                    title="Pipeline is paused — no new promotions will start"
                    style={{
                      fontSize: '0.6rem',
                      background: 'var(--color-accent-bg)',
                      color: 'var(--color-accent)',
                      border: '1px solid var(--color-accent)',
                      borderRadius: '3px',
                      padding: '0px 4px',
                      fontWeight: 700,
                      letterSpacing: '0.05em',
                    }}
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
              <div style={{ fontSize: '0.7rem', color: 'var(--color-text-muted)', display: 'flex', flexDirection: 'column', gap: '0.2rem' }}>
                {/* Multi-segment health bar when env states are available (#342) */}
                {p.environmentStates && Object.keys(p.environmentStates).length > 0 ? (
                  <div style={{ display: 'flex', gap: '0.3rem', alignItems: 'center', flexWrap: 'wrap' }}>
                    <span>{envCount} env{envCount !== 1 ? 's' : ''}</span>
                    <span style={{ color: 'var(--color-surface)' }}>·</span>
                    {/* State badges: count per phase */}
                    {(() => {
                      const counts: Record<string, number> = {}
                      for (const phase of Object.values(p.environmentStates!)) {
                        counts[phase] = (counts[phase] ?? 0) + 1
                      }
                      const phaseColor: Record<string, string> = {
                        Verified: 'var(--color-success)', Promoting: 'var(--color-accent)', WaitingForMerge: 'var(--color-accent)',
                        HealthChecking: 'var(--color-info)', Failed: 'var(--color-error)', Pending: 'var(--color-text-faint)',
                      }
                      return Object.entries(counts).map(([phase, count]) => (
                        <span key={phase} style={{
                          fontSize: '0.6rem',
                          color: phaseColor[phase] ?? 'var(--color-text-muted)',
                          fontWeight: 600,
                        }} title={`${count} env${count !== 1 ? 's' : ''} in ${phase}`}>
                          {count} {phase === 'WaitingForMerge' ? 'PR' : phase === 'HealthChecking' ? 'health' : phase.toLowerCase()}
                        </span>
                      ))
                    })()}
                  </div>
                ) : (
                  <div style={{ display: 'flex', gap: '0.4rem' }}>
                    {envCount > 0 && (
                      <span>{envCount} env{envCount !== 1 ? 's' : ''}</span>
                    )}
                    {bundle && (
                      <>
                        {envCount > 0 && <span>·</span>}
                        <span
                          style={{ fontFamily: 'monospace', color: 'var(--color-text-muted)' }}
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
                  <span style={{ fontFamily: 'monospace', color: 'var(--color-text-muted)' }} title={p.activeBundleName}>
                    {bundle}
                  </span>
                )}
              </div>
            )}
          </button>
          {/* #763: CopyButton as a sibling of the selection button — not nested inside it.
              Flex sibling renders as a narrow strip on the right of the row. */}
          <div style={{
            display: 'flex',
            alignItems: 'flex-start',
            paddingTop: '0.55rem',
            paddingRight: '0.4rem',
            background: selectedRow ? 'var(--color-surface)' : 'transparent',
          }}>
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
        style={{ listStyle: 'none', display: 'flex', alignItems: 'stretch' }}
      >
        {renderPipelineItemContent(p)}
      </li>
    )
  }
}
