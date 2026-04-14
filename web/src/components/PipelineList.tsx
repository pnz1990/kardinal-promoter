// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// components/PipelineList.tsx — Pipeline list with sortable health columns.
// #462: Adds sortable columns (Name, Age, Blocked), row health indicator,
//       and floats blocked pipelines to the top by default.
// #345: Debounced search/filter input at the top.
// #342: Multi-segment environment health bar per pipeline.
import { useState, useCallback, useRef } from 'react'
import type { Pipeline } from '../types'
import { HealthChip } from './HealthChip'

interface Props {
  pipelines: Pipeline[]
  selected?: string
  onSelect: (name: string) => void
  loading?: boolean
  error?: string
  /** Current namespace derived from loaded pipelines. Shown in header when set. */
  namespace?: string
}

/** #462: Sort columns for the pipeline operations view. */
export type SortColumn = 'name' | 'blocked' | 'age' | 'status'
export type SortDir = 'asc' | 'desc'

/** #462: Format age in seconds to a human-readable string. */
export function formatAge(seconds: number | undefined): string {
  if (!seconds || seconds <= 0) return '—'
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`
  return `${Math.floor(seconds / 86400)}d`
}

/** #462: Derive a row-level health color for a pipeline.
 * Returns 'red' (blocked/failed), 'yellow' (in-progress), 'green' (all verified),
 * 'gray' (no active bundle). */
export function pipelineRowHealth(p: Pipeline): 'red' | 'yellow' | 'green' | 'gray' {
  if ((p.blockedCount ?? 0) > 0) return 'red'
  if (p.paused) return 'yellow'
  const phase = p.phase
  if (phase === 'Verified') return 'green'
  if (phase === 'Promoting' || phase === 'WaitingForMerge' || phase === 'HealthChecking') return 'yellow'
  if (phase === 'Failed') return 'red'
  if (!p.activeBundleName) return 'gray'
  return 'gray'
}

const healthBorderColor: Record<string, string> = {
  red: '#ef4444',
  yellow: '#f59e0b',
  green: '#22c55e',
  gray: 'transparent',
}

/** #462: Sort pipelines by the given column.
 * Default: blocked pipelines float to top.
 *
 * For 'blocked' and 'status': desc = worst first (most blocked / worst health).
 * For 'name': asc = A→Z.
 * For 'age': asc = newest first (smallest age), desc = oldest first.
 */
export function sortPipelines(
  pipelines: Pipeline[],
  col: SortColumn,
  dir: SortDir,
): Pipeline[] {
  return [...pipelines].sort((a, b) => {
    switch (col) {
      case 'name': {
        const cmp = a.name.localeCompare(b.name)
        return dir === 'asc' ? cmp : -cmp
      }
      case 'blocked': {
        // desc = most blocked first
        const diff = (b.blockedCount ?? 0) - (a.blockedCount ?? 0)
        if (diff !== 0) return dir === 'desc' ? diff : -diff
        return a.name.localeCompare(b.name)
      }
      case 'age': {
        // desc = oldest first (largest age in seconds)
        const aa = a.lastBundleAgeSeconds ?? 0
        const bb = b.lastBundleAgeSeconds ?? 0
        const diff = aa - bb
        return dir === 'asc' ? diff : -diff
      }
      case 'status': {
        // desc = worst health first (red > yellow > green > gray)
        const healthOrder: Record<string, number> = { red: 3, yellow: 2, green: 1, gray: 0 }
        const ha = healthOrder[pipelineRowHealth(a)] ?? 0
        const hb = healthOrder[pipelineRowHealth(b)] ?? 0
        if (ha !== hb) return dir === 'desc' ? hb - ha : ha - hb
        return a.name.localeCompare(b.name)
      }
      default:
        return 0
    }
  })
}

/** Truncate a bundle name to a readable short form for the sidebar. */
function shortBundleName(name: string | undefined): string | null {
  if (!name) return null
  if (name.length <= 14) return name
  return name.slice(0, 12) + '…'
}

/** Onboarding empty state shown when no pipelines have been created yet. */
function EmptyState() {
  return (
    <div style={{ padding: '1rem', color: '#94a3b8', fontSize: '0.8rem' }}>
      <p style={{ marginBottom: '0.75rem', fontStyle: 'italic' }}>No pipelines found.</p>
      <p style={{ marginBottom: '0.5rem', color: '#64748b' }}>Get started:</p>
      <code style={{
        display: 'block',
        background: '#0f172a',
        border: '1px solid #1e293b',
        borderRadius: '4px',
        padding: '0.4rem 0.5rem',
        fontSize: '0.72rem',
        color: '#7dd3fc',
        marginBottom: '0.5rem',
        whiteSpace: 'pre-wrap',
        wordBreak: 'break-all',
      }}>
        kubectl apply -f examples/quickstart/pipeline.yaml
      </code>
      <p style={{ marginBottom: '0.4rem', color: '#64748b' }}>Or use the wizard:</p>
      <code style={{
        display: 'block',
        background: '#0f172a',
        border: '1px solid #1e293b',
        borderRadius: '4px',
        padding: '0.4rem 0.5rem',
        fontSize: '0.72rem',
        color: '#7dd3fc',
        marginBottom: '0.75rem',
      }}>
        kardinal init
      </code>
      <a
        href="https://github.com/pnz1990/kardinal-promoter/blob/main/docs/quickstart.md"
        target="_blank"
        rel="noopener noreferrer"
        style={{ color: '#6366f1', fontSize: '0.75rem', textDecoration: 'none' }}
        aria-label="View quickstart documentation"
      >
        View quickstart docs ↗
      </a>
    </div>
  )
}

/** #462: Sort header button. */
function SortHeader({
  col, label, current, dir, onClick,
}: {
  col: SortColumn
  label: string
  current: SortColumn
  dir: SortDir
  onClick: (col: SortColumn) => void
}) {
  const isActive = current === col
  return (
    <button
      onClick={() => onClick(col)}
      aria-label={`Sort by ${label}${isActive ? (dir === 'asc' ? ', ascending' : ', descending') : ''}`}
      style={{
        background: 'none',
        border: 'none',
        cursor: 'pointer',
        color: isActive ? '#a5b4fc' : '#475569',
        fontSize: '0.62rem',
        fontWeight: isActive ? 700 : 400,
        padding: '0 0.25rem',
        textTransform: 'uppercase',
        letterSpacing: '0.04em',
        display: 'inline-flex',
        alignItems: 'center',
        gap: '0.15rem',
      }}
    >
      {label}
      {isActive && <span style={{ fontSize: '0.7em' }}>{dir === 'asc' ? '↑' : '↓'}</span>}
    </button>
  )
}

export function PipelineList({ pipelines, selected, onSelect, loading, error }: Props) {
  // #345: search/filter state with debounce
  const [searchQuery, setSearchQuery] = useState('')
  const debounceTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [debouncedQuery, setDebouncedQuery] = useState('')

  // #462: sort state — default: blocked pipelines float to top
  const [sortCol, setSortCol] = useState<SortColumn>('blocked')
  const [sortDir, setSortDir] = useState<SortDir>('desc')

  const handleSearchChange = useCallback((value: string) => {
    setSearchQuery(value)
    if (debounceTimer.current) clearTimeout(debounceTimer.current)
    debounceTimer.current = setTimeout(() => {
      setDebouncedQuery(value.trim().toLowerCase())
    }, 150)
  }, [])

  const handleSortClick = useCallback((col: SortColumn) => {
    setSortCol(prev => {
      if (prev === col) {
        setSortDir(d => d === 'asc' ? 'desc' : 'asc')
        return prev
      }
      setSortDir('desc')
      return col
    })
  }, [])

  if (loading) {
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
              background: 'linear-gradient(90deg, #1e293b 25%, #293548 50%, #1e293b 75%)',
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
      <div style={{ padding: '1rem', color: '#ef4444', fontSize: '0.82rem' }}>
        Error: {error}
      </div>
    )
  }
  if (pipelines.length === 0) {
    return <EmptyState />
  }

  // #345: filter pipelines by search query
  const filteredPipelines = debouncedQuery
    ? pipelines.filter(p =>
        p.name.toLowerCase().includes(debouncedQuery) ||
        p.namespace.toLowerCase().includes(debouncedQuery) ||
        `${p.namespace}/${p.name}`.toLowerCase().includes(debouncedQuery)
      )
    : pipelines

  // #462: sort filtered list
  const sortedPipelines = sortPipelines(filteredPipelines, sortCol, sortDir)

  // #358: detect multi-namespace setup
  const uniqueNamespaces = new Set(pipelines.map(p => p.namespace))
  const isMultiNamespace = uniqueNamespaces.size > 1

  // Group sorted list by namespace for multi-namespace display
  const pipelinesByNamespace: Record<string, typeof sortedPipelines> = {}
  for (const p of sortedPipelines) {
    if (!pipelinesByNamespace[p.namespace]) pipelinesByNamespace[p.namespace] = []
    pipelinesByNamespace[p.namespace].push(p)
  }
  const namespaceOrder = Array.from(uniqueNamespaces).sort()

  return (
    <div>
      {/* #345: search/filter input */}
      {pipelines.length > 3 && (
        <div style={{ padding: '0.5rem 1rem 0.25rem', position: 'relative' }}>
          <input
            type="text"
            placeholder={isMultiNamespace ? "Filter by name or namespace…" : "Filter pipelines…"}
            value={searchQuery}
            onChange={e => handleSearchChange(e.target.value)}
            aria-label="Filter pipelines by name or namespace"
            style={{
              width: '100%',
              boxSizing: 'border-box',
              background: '#1e293b',
              border: '1px solid #334155',
              borderRadius: '4px',
              padding: '0.3rem 1.75rem 0.3rem 0.5rem',
              fontSize: '0.78rem',
              color: '#e2e8f0',
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
                color: '#64748b',
                fontSize: '0.9rem',
                padding: '0 2px',
                lineHeight: 1,
              }}
            >×</button>
          )}
        </div>
      )}

      {/* #462: Sort header bar */}
      {pipelines.length > 1 && (
        <div style={{
          display: 'flex',
          alignItems: 'center',
          gap: '0.1rem',
          padding: '0.15rem 1rem',
          borderBottom: '1px solid #1e293b',
        }}>
          <SortHeader col="status" label="Status" current={sortCol} dir={sortDir} onClick={handleSortClick} />
          <span style={{ color: '#1e293b' }}>|</span>
          <SortHeader col="name" label="Name" current={sortCol} dir={sortDir} onClick={handleSortClick} />
          <span style={{ color: '#1e293b' }}>|</span>
          <SortHeader col="blocked" label="Blocked" current={sortCol} dir={sortDir} onClick={handleSortClick} />
          <span style={{ color: '#1e293b' }}>|</span>
          <SortHeader col="age" label="Age" current={sortCol} dir={sortDir} onClick={handleSortClick} />
        </div>
      )}

      <ul style={{ listStyle: 'none', padding: 0, margin: 0 }}>
        {sortedPipelines.length === 0 && debouncedQuery && (
          <li style={{ padding: '0.75rem 1rem', color: '#64748b', fontSize: '0.8rem' }}>
            No pipelines match "{debouncedQuery}"
          </li>
        )}
        {/* #358: multi-namespace grouped display */}
        {isMultiNamespace ? (
          namespaceOrder.map(ns => {
            const nsPipelines = pipelinesByNamespace[ns]
            if (!nsPipelines || nsPipelines.length === 0) return null
            return (
              <li key={ns}>
                <div style={{
                  padding: '0.3rem 1rem 0.15rem',
                  fontSize: '0.65rem',
                  color: '#475569',
                  textTransform: 'uppercase',
                  letterSpacing: '0.05em',
                  borderTop: '1px solid #1e293b',
                  fontFamily: 'monospace',
                  background: '#070f1b',
                }}>
                  {ns}
                </div>
                <ul style={{ listStyle: 'none', padding: 0, margin: 0 }}>
                  {nsPipelines.map(p => renderPipelineItem(p))}
                </ul>
              </li>
            )
          })
        ) : (
          sortedPipelines.map(p => renderPipelineItem(p))
        )}
      </ul>
    </div>
  )

  function renderPipelineItem(p: Pipeline) {
    const bundle = shortBundleName(p.activeBundleName)
    const envCount = p.environmentCount
    const rowHealth = pipelineRowHealth(p)
    const borderColor = healthBorderColor[rowHealth]
    const ageStr = formatAge(p.lastBundleAgeSeconds)

    return (
      <li
        key={`${p.namespace}/${p.name}`}
        onClick={() => onSelect(p.name)}
        role="button"
        aria-selected={selected === p.name}
        tabIndex={0}
        onKeyDown={e => (e.key === 'Enter' || e.key === ' ') && onSelect(p.name)}
        style={{
          padding: '0.6rem 1rem',
          cursor: 'pointer',
          background: selected === p.name ? '#1e293b' : 'transparent',
          borderLeft: selected === p.name
            ? `3px solid #6366f1`
            : `3px solid ${borderColor}`,
        }}
      >
        {/* Pipeline name + phase badge */}
        <div style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginBottom: bundle || envCount ? '0.2rem' : 0,
        }}>
          <span style={{
            fontWeight: selected === p.name ? 600 : 400,
            fontSize: '0.85rem',
            color: '#e2e8f0',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
            maxWidth: '120px',
          }}>
            {p.name}
          </span>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.25rem' }}>
            {/* #462: blocked badge */}
            {(p.blockedCount ?? 0) > 0 && (
              <span
                title={`${p.blockedCount} environment${p.blockedCount !== 1 ? 's' : ''} failed`}
                style={{
                  fontSize: '0.6rem',
                  background: '#7f1d1d',
                  color: '#fca5a5',
                  border: '1px solid #dc2626',
                  borderRadius: '3px',
                  padding: '0px 4px',
                  fontWeight: 700,
                }}
              >
                {p.blockedCount} FAIL
              </span>
            )}
            {/* Paused badge */}
            {p.paused && (
              <span
                title="Pipeline is paused — no new promotions will start"
                style={{
                  fontSize: '0.6rem',
                  background: '#1e1b4b',
                  color: '#a5b4fc',
                  border: '1px solid #4338ca',
                  borderRadius: '3px',
                  padding: '0px 4px',
                  fontWeight: 700,
                  letterSpacing: '0.05em',
                }}
              >
                PAUSED
              </span>
            )}
            {p.phase && <HealthChip state={p.paused ? 'Paused' : p.phase} size="sm" />}
          </div>
        </div>

        {/* Sub-line: env count + health bar + bundle + age (#342 + #462) */}
        {(bundle || envCount > 0) && (
          <div style={{ fontSize: '0.7rem', color: '#64748b', display: 'flex', flexDirection: 'column', gap: '0.2rem' }}>
            {p.environmentStates && Object.keys(p.environmentStates).length > 0 ? (
              <div style={{ display: 'flex', gap: '0.3rem', alignItems: 'center', flexWrap: 'wrap' }}>
                <span>{envCount} env{envCount !== 1 ? 's' : ''}</span>
                <span style={{ color: '#1e293b' }}>·</span>
                {(() => {
                  const counts: Record<string, number> = {}
                  for (const phase of Object.values(p.environmentStates!)) {
                    counts[phase] = (counts[phase] ?? 0) + 1
                  }
                  const phaseColor: Record<string, string> = {
                    Verified: '#22c55e', Promoting: '#6366f1', WaitingForMerge: '#6366f1',
                    HealthChecking: '#a78bfa', Failed: '#ef4444', Pending: '#475569',
                  }
                  return Object.entries(counts).map(([phase, count]) => (
                    <span key={phase} style={{
                      fontSize: '0.6rem',
                      color: phaseColor[phase] ?? '#64748b',
                      fontWeight: 600,
                    }} title={`${count} env${count !== 1 ? 's' : ''} in ${phase}`}>
                      {count} {phase === 'WaitingForMerge' ? 'PR' : phase === 'HealthChecking' ? 'health' : phase.toLowerCase()}
                    </span>
                  ))
                })()}
                {/* #462: age column */}
                {ageStr !== '—' && (
                  <>
                    <span style={{ color: '#1e293b' }}>·</span>
                    <span title="Age of active bundle" style={{ color: '#475569' }}>{ageStr}</span>
                  </>
                )}
              </div>
            ) : (
              <div style={{ display: 'flex', gap: '0.4rem' }}>
                {envCount > 0 && <span>{envCount} env{envCount !== 1 ? 's' : ''}</span>}
                {bundle && (
                  <>
                    {envCount > 0 && <span>·</span>}
                    <span style={{ fontFamily: 'monospace', color: '#94a3b8' }} title={p.activeBundleName}>
                      {bundle}
                    </span>
                  </>
                )}
                {ageStr !== '—' && (
                  <>
                    <span>·</span>
                    <span title="Age of active bundle" style={{ color: '#475569' }}>{ageStr}</span>
                  </>
                )}
              </div>
            )}
            {p.environmentStates && bundle && (
              <span style={{ fontFamily: 'monospace', color: '#94a3b8' }} title={p.activeBundleName}>
                {bundle}
              </span>
            )}
          </div>
        )}
      </li>
    )
  }
}
