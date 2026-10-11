// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/PipelineOpsTable.tsx — Sortable/filterable operations table for
// the pipeline list page (#462). Shows operators the signals they need to triage
// at a glance: blocked gates, failed steps, stale inventory, last merge time.

import { useState, useMemo } from 'react'
import type { Pipeline } from '../types'
import { HealthChip } from './HealthChip'
import { Icon } from '../ui/Icon'
import { ErrorNotice } from '../ui/ErrorNotice'
import '../styles/PipelineOpsTable.css'

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
  /** Read the pipelines again (the error's Retry). */
  onRetry?: () => void
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

/** Staleness of the inventory age: verified (fresh), blocked (over 14 days), failed (over 30). */
function inventoryTone(days: number | undefined): 'muted' | 'failed' | 'blocked' | 'verified' {
  if (days === undefined) return 'muted'
  if (days > 30) return 'failed'
  if (days > 14) return 'blocked'
  return 'verified'
}

interface Column {
  key: SortColumn
  label: string
  title?: string
}

const COLUMNS: Column[] = [
  { key: 'name', label: 'Pipeline' },
  { key: 'phase', label: 'Status' },
  { key: 'blockerCount', label: 'Blocking gates', title: 'Gates holding a version back right now' },
  { key: 'failedStepCount', label: 'Failed stages', title: 'Stages whose promotion failed' },
  { key: 'inventoryAgeDays', label: 'Version age', title: 'Days since the newest version was shipped' },
  { key: 'lastMergedAt', label: 'Last merge', title: 'When an environment last reached Verified' },
]

export function PipelineOpsTable({ pipelines, selected, selectedNamespace, onSelect, loading, error, onRetry }: Props) {
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
      <div className="ops-table__loading">
        Loading pipelines…
      </div>
    )
  }
  if (error) {
    return (
      <ErrorNotice error={error} onRetry={onRetry} className="ops-table__error" />
    )
  }

  return (
    <div className="ops-table">
      {/* Filter bar */}
      <div className="ops-table__filter-bar">
        <input
          type="text"
          placeholder="Filter pipelines…"
          value={filter}
          onChange={e => setFilter(e.target.value)}
          aria-label="Filter pipelines"
          className="ops-table__filter"
        />
        <span className="ops-table__count">
          {sorted.length} pipeline{sorted.length !== 1 ? 's' : ''}
          {filter ? ` matching "${filter}"` : ''}
        </span>
      </div>

      {/* Table */}
      <div className="ops-table__scroll">
        <table
          className="ops-table__table"
          aria-label="Pipeline operations table"
        >
          <thead>
            <tr>
              {COLUMNS.map(col => (
                <th
                  key={col.key}
                  title={col.title}
                  onClick={() => handleSort(col.key)}
                  className="ops-table__th"
                  aria-sort={
                    sortCol === col.key
                      ? sortDir === 'asc' ? 'ascending' : 'descending'
                      : 'none'
                  }
                >
                  {col.label}
                  {sortCol === col.key && (
                    <span className="ops-table__sort">
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
                  className="ops-table__td ops-table__empty"
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
                  className="ops-table__row"
                >
                  {/* Pipeline name */}
                  <td className="ops-table__td">
                    <div className="ops-table__name-row">
                      <span className="ops-table__name">{p.name}</span>
                      {p.paused && (
                        <span className="ops-table__paused">
                          PAUSED
                        </span>
                      )}
                    </div>
                    {p.activeBundleName && (
                      <div className="ops-table__bundle">
                        {p.activeBundleName.length > 20
                          ? p.activeBundleName.slice(0, 18) + '…'
                          : p.activeBundleName}
                      </div>
                    )}
                  </td>

                  {/* Status */}
                  <td className="ops-table__td">
                    <HealthChip state={p.paused ? 'Paused' : p.phase} kind="pipeline" size="sm" />
                  </td>

                  {/* Blockers */}
                  <td className="ops-table__td" data-tone={hasBlockers ? 'failed' : 'verified'}>
                    {hasBlockers ? (
                      <span title={`${p.blockerCount} gate${p.blockerCount === 1 ? '' : 's'} blocking`}>
                        <Icon name="gate" className="ops-table__icon" /> {p.blockerCount}
                      </span>
                    ) : (
                      <span title="No blockers">0</span>
                    )}
                  </td>

                  {/* Failed steps */}
                  <td className="ops-table__td" data-tone={hasFailed ? 'failed' : 'verified'}>
                    {hasFailed ? (
                      <span title={`${p.failedStepCount} failed step${p.failedStepCount === 1 ? '' : 's'}`}>
                        ✗ {p.failedStepCount}
                      </span>
                    ) : (
                      <span>0</span>
                    )}
                  </td>

                  {/* Inventory age */}
                  <td className="ops-table__td" data-tone={inventoryTone(p.inventoryAgeDays)}>
                    {p.inventoryAgeDays !== undefined ? (
                      <span title={`${p.inventoryAgeDays} day${p.inventoryAgeDays === 1 ? '' : 's'} since last bundle`}>
                        {p.inventoryAgeDays === 0 ? 'today' : `${p.inventoryAgeDays}d`}
                        {isStale && <span className="ops-table__stale">⚠</span>}
                      </span>
                    ) : '—'}
                  </td>

                  {/* Last merge */}
                  <td className="ops-table__td">
                    <span title={p.lastMergedAt ?? 'Never merged'} className="ops-table__muted">
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
