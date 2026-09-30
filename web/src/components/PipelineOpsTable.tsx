// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/PipelineOpsTable.tsx — Sortable/filterable operations table for
// the pipeline list page (#462). Shows operators the signals they need to triage
// at a glance: blocked gates, failed steps, stale inventory, last merge time.

import { useState, useMemo } from 'react'
import type { Pipeline } from '../types'
import { HealthChip } from './HealthChip'

type SortColumn =
  | 'name'
  | 'phase'
  | 'blockerCount'
  | 'failedStepCount'
  | 'inventoryAgeDays'
  | 'lastMergedAt'

type SortDir = 'asc' | 'desc'

interface Props {
  pipelines: Pipeline[]
  /** Name of the selected pipeline. */
  selected?: string
  /** Namespace of the selected pipeline; pipelines are identified by namespace + name. */
  selectedNamespace?: string
  onSelect: (name: string, namespace: string) => void
  loading?: boolean
  error?: string
}

/** Format a relative time string from an ISO timestamp. */
function relativeTime(iso: string | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '—'
  const diffMs = Date.now() - d.getTime()
  const diffDays = Math.floor(diffMs / 86400000)
  if (diffDays === 0) return 'today'
  if (diffDays === 1) return '1d ago'
  if (diffDays < 30) return `${diffDays}d ago`
  const diffMonths = Math.floor(diffDays / 30)
  return `${diffMonths}mo ago`
}

/** Red/amber/green staleness color for inventory age. */
function inventoryColor(days: number | undefined): string {
  if (days === undefined) return 'var(--color-text-muted)'
  if (days > 30) return 'var(--color-error)'
  if (days > 14) return 'var(--color-warning)'
  return 'var(--color-success)'
}

const CELL: React.CSSProperties = {
  padding: '0.45rem 0.75rem',
  borderBottom: '1px solid var(--color-border-muted)',
  fontSize: '0.82rem',
  color: 'var(--color-text)',
  whiteSpace: 'nowrap',
  verticalAlign: 'middle',
}

const HEADER_CELL: React.CSSProperties = {
  ...CELL,
  color: 'var(--color-text-muted)',
  fontSize: '0.72rem',
  textTransform: 'uppercase' as const,
  letterSpacing: '0.05em',
  background: 'var(--color-bg-deep)',
  borderBottom: '1px solid var(--color-border)',
  cursor: 'pointer',
  userSelect: 'none' as const,
}

interface Column {
  key: SortColumn
  label: string
  title?: string
}

const COLUMNS: Column[] = [
  { key: 'name', label: 'Pipeline' },
  { key: 'phase', label: 'Status' },
  { key: 'blockerCount', label: 'Blockers', title: 'PolicyGates currently blocking this pipeline' },
  { key: 'failedStepCount', label: 'Failed Steps', title: 'PromotionSteps in Failed state' },
  { key: 'inventoryAgeDays', label: 'Inventory Age', title: 'Days since latest bundle was created' },
  { key: 'lastMergedAt', label: 'Last Merge', title: 'When the last environment reached Verified' },
]

export function PipelineOpsTable({ pipelines, selected, selectedNamespace, onSelect, loading, error }: Props) {
  const [sortCol, setSortCol] = useState<SortColumn>('blockerCount')
  const [sortDir, setSortDir] = useState<SortDir>('desc')
  const [filter, setFilter] = useState('')

  const handleSort = (col: SortColumn) => {
    if (sortCol === col) {
      setSortDir(d => d === 'asc' ? 'desc' : 'asc')
    } else {
      setSortCol(col)
      setSortDir('desc')
    }
  }

  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase()
    return q
      ? pipelines.filter(p =>
          p.name.toLowerCase().includes(q) ||
          p.namespace.toLowerCase().includes(q) ||
          p.phase.toLowerCase().includes(q)
        )
      : pipelines
  }, [pipelines, filter])

  const sorted = useMemo(() => {
    return [...filtered].sort((a, b) => {
      let cmp = 0
      switch (sortCol) {
        case 'name':
          cmp = a.name.localeCompare(b.name)
          break
        case 'phase':
          cmp = (a.phase ?? '').localeCompare(b.phase ?? '')
          break
        case 'blockerCount':
          cmp = (a.blockerCount ?? 0) - (b.blockerCount ?? 0)
          break
        case 'failedStepCount':
          cmp = (a.failedStepCount ?? 0) - (b.failedStepCount ?? 0)
          break
        case 'inventoryAgeDays':
          cmp = (a.inventoryAgeDays ?? 0) - (b.inventoryAgeDays ?? 0)
          break
        case 'lastMergedAt':
          cmp = (a.lastMergedAt ?? '').localeCompare(b.lastMergedAt ?? '')
          break
      }
      return sortDir === 'asc' ? cmp : -cmp
    })
  }, [filtered, sortCol, sortDir])

  if (loading) {
    return (
      <div style={{ padding: '1.5rem', color: 'var(--color-text-muted)', fontSize: '0.85rem' }}>
        Loading pipelines…
      </div>
    )
  }
  if (error) {
    return (
      <div role="alert" style={{ padding: '1rem', color: 'var(--color-error)', fontSize: '0.82rem' }}>
        Error: {error}
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      {/* Filter bar */}
      <div style={{
        padding: '0.6rem 1rem',
        borderBottom: '1px solid var(--color-border-muted)',
        background: 'var(--color-bg-deep)',
        display: 'flex',
        alignItems: 'center',
        gap: '0.5rem',
      }}>
        <input
          type="text"
          placeholder="Filter pipelines…"
          value={filter}
          onChange={e => setFilter(e.target.value)}
          aria-label="Filter pipelines"
          style={{
            background: 'var(--color-surface)',
            border: '1px solid var(--color-border)',
            borderRadius: '4px',
            padding: '0.3rem 0.6rem',
            fontSize: '0.8rem',
            color: 'var(--color-text)',
            outline: 'none',
            width: '220px',
          }}
        />
        <span style={{ fontSize: '0.75rem', color: 'var(--color-text-faint)' }}>
          {sorted.length} pipeline{sorted.length !== 1 ? 's' : ''}
          {filter ? ` matching "${filter}"` : ''}
        </span>
      </div>

      {/* Table */}
      <div style={{ flex: 1, overflow: 'auto' }}>
        <table
          style={{
            width: '100%',
            borderCollapse: 'collapse',
            fontFamily: 'inherit',
          }}
          aria-label="Pipeline operations table"
        >
          <thead>
            <tr>
              {COLUMNS.map(col => (
                <th
                  key={col.key}
                  title={col.title}
                  onClick={() => handleSort(col.key)}
                  style={HEADER_CELL}
                  aria-sort={
                    sortCol === col.key
                      ? sortDir === 'asc' ? 'ascending' : 'descending'
                      : 'none'
                  }
                >
                  {col.label}
                  {sortCol === col.key && (
                    <span style={{ marginLeft: '0.3rem' }}>
                      {sortDir === 'asc' ? '↑' : '↓'}
                    </span>
                  )}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {sorted.length === 0 && (
              <tr>
                <td
                  colSpan={COLUMNS.length}
                  style={{ ...CELL, color: 'var(--color-text-faint)', textAlign: 'center', padding: '2rem' }}
                >
                  {filter ? `No pipelines match "${filter}"` : 'No pipelines found.'}
                </td>
              </tr>
            )}
            {sorted.map(p => {
              const isSelected = selected === p.name
                && (selectedNamespace === undefined || selectedNamespace === p.namespace)
              const hasBlockers = (p.blockerCount ?? 0) > 0
              const hasFailed = (p.failedStepCount ?? 0) > 0
              const isStale = (p.inventoryAgeDays ?? 0) > 14

              return (
                <tr
                  key={`${p.namespace}/${p.name}`}
                  onClick={() => onSelect(p.name, p.namespace)}
                  tabIndex={0}
                  onKeyDown={e => {
                    if (e.key !== 'Enter' && e.key !== ' ') return
                    e.preventDefault() // Space would otherwise scroll the table
                    onSelect(p.name, p.namespace)
                  }}
                  aria-selected={isSelected}
                  style={{
                    cursor: 'pointer',
                    background: isSelected ? 'var(--color-surface)' : 'transparent',
                    borderLeft: isSelected ? '3px solid var(--color-accent)' : '3px solid transparent',
                  }}
                >
                  {/* Pipeline name */}
                  <td style={CELL}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.4rem' }}>
                      <span style={{ fontWeight: isSelected ? 600 : 400 }}>{p.name}</span>
                      {p.paused && (
                        <span style={{
                          fontSize: '0.6rem',
                          background: 'var(--color-accent-bg)',
                          color: 'var(--color-accent)',
                          border: '1px solid var(--color-accent)',
                          borderRadius: '3px',
                          padding: '0px 4px',
                          fontWeight: 700,
                        }}>
                          PAUSED
                        </span>
                      )}
                    </div>
                    {p.activeBundleName && (
                      <div style={{
                        fontSize: '0.68rem',
                        color: 'var(--color-text-faint)',
                        fontFamily: 'monospace',
                        marginTop: '0.15rem',
                      }}>
                        {p.activeBundleName.length > 20
                          ? p.activeBundleName.slice(0, 18) + '…'
                          : p.activeBundleName}
                      </div>
                    )}
                  </td>

                  {/* Status */}
                  <td style={CELL}>
                    <HealthChip state={p.paused ? 'Paused' : p.phase} size="sm" />
                  </td>

                  {/* Blockers */}
                  <td style={{ ...CELL, color: hasBlockers ? 'var(--color-error)' : 'var(--color-success)' }}>
                    {hasBlockers ? (
                      <span title={`${p.blockerCount} gate${p.blockerCount === 1 ? '' : 's'} blocking`}>
                        ⛔ {p.blockerCount}
                      </span>
                    ) : (
                      <span title="No blockers">0</span>
                    )}
                  </td>

                  {/* Failed steps */}
                  <td style={{ ...CELL, color: hasFailed ? 'var(--color-error)' : 'var(--color-success)' }}>
                    {hasFailed ? (
                      <span title={`${p.failedStepCount} failed step${p.failedStepCount === 1 ? '' : 's'}`}>
                        ✗ {p.failedStepCount}
                      </span>
                    ) : (
                      <span>0</span>
                    )}
                  </td>

                  {/* Inventory age */}
                  <td style={{ ...CELL, color: inventoryColor(p.inventoryAgeDays) }}>
                    {p.inventoryAgeDays !== undefined ? (
                      <span title={`${p.inventoryAgeDays} day${p.inventoryAgeDays === 1 ? '' : 's'} since last bundle`}>
                        {p.inventoryAgeDays === 0 ? 'today' : `${p.inventoryAgeDays}d`}
                        {isStale && <span style={{ marginLeft: '0.3rem' }}>⚠</span>}
                      </span>
                    ) : '—'}
                  </td>

                  {/* Last merge */}
                  <td style={CELL}>
                    <span title={p.lastMergedAt ?? 'Never merged'} style={{ color: 'var(--color-text-muted)' }}>
                      {relativeTime(p.lastMergedAt)}
                    </span>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}
