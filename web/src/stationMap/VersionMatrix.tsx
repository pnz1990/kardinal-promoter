// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stationMap/VersionMatrix.tsx — the pipeline as a matrix (DESIGN-SYSTEM §3.5): one
// row per version (Bundle), newest first, one column per environment, where a cell
// is the version's state there. It answers "where did 1.4.2 reach?" for many
// versions at once. Columns collapse into waves and fleets like the map (a group's
// cell counts its members); rows are virtualized, so 200 versions × 300
// environments scroll as fast as 20.

import { memo, useCallback, useMemo, useRef, useState } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import type { Bundle } from '../types'
import type { Group, StationMapModel } from './model'
import { TONE_ORDER } from './model'
import { displayState, type DisplayState } from './display'
import { StatusChip } from '../ui'
import { VersionLabel } from '../components/VersionLabel'
import { bundleVersion } from '../stage/stageModel'
import '../styles/StageTable.css'

/** A row's height: --k-space-8 at the default density. */
const ROW_H = 32
/** Rows rendered beyond the visible ones, so a fast scroll does not show blanks. */
const OVERSCAN = 8
/** Rows in a first window, before the scroller is measured. */
const VIEW_ROWS = 20

export interface VersionMatrixProps {
  model: StationMapModel
  pipeline: string
  /** Newest first. */
  bundles: Bundle[]
  /** The version followed on the map, and the one compared with it. */
  followed?: string
  compared?: string
  /** Click: follow the version; Shift-click: compare it with the followed one. */
  onSelect: (bundle: string) => void
  onCompare: (bundle: string) => void
  onToggleGroup: (id: string) => void
}

type Column = { id: string; label: string; envs: string[]; group?: Group; expandedFrom?: string }

/** A column's width in px: its header's text (--k-text-2xs caps) and a count for a group. */
const CH_PX = 7.5
const COL_PAD = 24
const MIN_COL = 56
/** The sticky version column's width. */
const VERSION_W = 220
/** Columns rendered beyond the visible ones. */
const COL_OVERSCAN = 4

function colWidth(c: Column): number {
  return Math.max(MIN_COL, Math.ceil(c.label.length * CH_PX) + COL_PAD + (c.group ? CH_PX * 2 : 0))
}

/** A window of n items of fixed sizes before the scroller is measured (and in jsdom). */
function firstWindow(sizes: number[], n: number) {
  let start = 0
  return sizes.slice(0, n).map((size, index) => {
    const item = { index, start, end: start + size, key: index, size, lane: 0 }
    start += size
    return item
  })
}

export default function VersionMatrix({ model, pipeline, bundles, followed, compared, onSelect, onCompare, onToggleGroup }: VersionMatrixProps) {
  const scroller = useRef<HTMLDivElement | null>(null)
  const columns = useMemo(() => columnsOf(model), [model])
  const widths = useMemo(() => columns.map(colWidth), [columns])
  const states = useMemo(() => bundles.map(b => new Map((b.environments ?? []).map(e => [e.name, e.phase ?? '']))), [bundles])
  // Rows and columns are both virtualized: 200 versions × 300 environments draw only what is in view.
  const rowsV = useVirtualizer({
    count: bundles.length,
    getScrollElement: () => scroller.current,
    estimateSize: () => ROW_H,
    overscan: OVERSCAN,
    initialRect: { width: ROW_H * VIEW_ROWS, height: ROW_H * VIEW_ROWS },
  })
  const colsV = useVirtualizer({
    horizontal: true,
    count: columns.length,
    getScrollElement: () => scroller.current,
    estimateSize: i => widths[i],
    overscan: COL_OVERSCAN,
    paddingStart: VERSION_W,
    initialRect: { width: ROW_H * VIEW_ROWS * 2, height: ROW_H * VIEW_ROWS },
  })
  const measuredRows = rowsV.getVirtualItems()
  const rows = measuredRows.length > 0 || bundles.length === 0 ? measuredRows
    : firstWindow(bundles.map(() => ROW_H), VIEW_ROWS + OVERSCAN)
  const measuredCols = colsV.getVirtualItems()
  const cols = measuredCols.length > 0 || columns.length === 0 ? measuredCols.map(c => ({ ...c, start: c.start - VERSION_W, end: c.end - VERSION_W }))
    : firstWindow(widths, VIEW_ROWS)
  const rowsBefore = rows.length ? rows[0].start : 0
  const rowsAfter = rows.length ? Math.max(0, bundles.length * ROW_H - rows[rows.length - 1].end) : 0
  const totalW = widths.reduce((a, w) => a + w, 0)
  const colsBefore = cols.length ? cols[0].start : 0
  const colsAfter = cols.length ? Math.max(0, totalW - cols[cols.length - 1].end) : 0

  // Sizes computed from the scroll position are set on the elements, not as inline styles.
  const size = useCallback((prop: 'block-size' | 'inline-size', px: number) => (el: HTMLElement | null) => {
    el?.style.setProperty(prop, `${px}px`)
    if (prop === 'inline-size') { el?.style.setProperty('min-inline-size', `${px}px`); el?.style.setProperty('max-inline-size', `${px}px`) }
  }, [])

  // The cell in focus (roving tabindex): arrows move by row and column, Enter follows its
  // version, Shift+Enter compares it. Its words also show under the matrix.
  const [cursor, setCursor] = useState<{ r: number; c: number }>({ r: 0, c: 0 })
  const focusCell = (r: number, c: number) => {
    const rr = Math.max(0, Math.min(bundles.length - 1, r))
    const cc = Math.max(0, Math.min(columns.length - 1, c))
    setCursor({ r: rr, c: cc })
    rowsV.scrollToIndex(rr)
    colsV.scrollToIndex(cc)
    requestAnimationFrame(() => scroller.current?.querySelector<HTMLElement>(`[data-cell="${rr}:${cc}"]`)?.focus())
  }
  const onCellKey = (e: React.KeyboardEvent, r: number, c: number) => {
    const page = VIEW_ROWS
    const moves: Record<string, [number, number]> = {
      ArrowUp: [r - 1, c], ArrowDown: [r + 1, c], ArrowLeft: [r, c - 1], ArrowRight: [r, c + 1],
      Home: [r, 0], End: [r, columns.length - 1], PageUp: [r - page, c], PageDown: [r + page, c],
    }
    if (moves[e.key]) { e.preventDefault(); focusCell(...moves[e.key]); return }
    if (e.key === 'Enter') { e.preventDefault(); (e.shiftKey ? onCompare : onSelect)(bundles[r].name) }
  }
  // The grid keeps one Tab stop: the cursor's cell, or the first one drawn when the cursor's
  // row or column has scrolled out of view.
  const shown = rows.some(x => x.index === cursor.r) && cols.some(x => x.index === cursor.c)
  const stop = shown || !rows.length || !cols.length ? cursor : { r: rows[0].index, c: cols[0].index }
  const at = bundles[cursor.r]
  const atCol = columns[cursor.c]
  const status = at && atCol ? `${bundleVersion(at)} in ${atCol.label}: ${cellText(atCol, states[cursor.r])}` : ''

  return (
    <div className="version-matrix-wrap">
    <div ref={scroller} className="version-matrix" role="region" aria-label="Versions by environment">
      {/* One handler for every cell (each cell names itself in data-cell): scrolling re-renders few props. */}
      <table ref={size('inline-size', VERSION_W + totalW)} className="version-matrix__table" role="grid" aria-rowcount={bundles.length + 1} aria-colcount={columns.length + 1}
        onFocus={e => { const rc = cellOf(e.target); if (rc) setCursor(rc) }}
        onKeyDown={e => { const rc = cellOf(e.target); if (rc) onCellKey(e, rc.r, rc.c) }}>
        <caption className="sr-only">
          Each of the {bundles.length} versions of {pipeline}, newest first, and its state in each environment.
          Arrow keys move between cells; Enter follows a version on the map, Shift+Enter compares it with the followed version.
        </caption>
        <thead>
          <tr aria-rowindex={1}>
            <th scope="col" ref={size('inline-size', VERSION_W)} className="version-matrix__head version-matrix__corner">Version</th>
            {colsBefore > 0 && <th ref={size('inline-size', colsBefore)} aria-hidden="true" className="version-matrix__gap" />}
            {cols.map(ci => {
              const c = columns[ci.index]
              return (
                <th key={c.id} scope="col" ref={size('inline-size', widths[ci.index])} aria-colindex={ci.index + 2} className="version-matrix__head">
                  {c.group
                    ? <button type="button" aria-expanded={false} onClick={() => onToggleGroup(c.group!.id)}>{`${c.label} ▸`}</button>
                    : c.expandedFrom
                      ? <button type="button" aria-expanded={true} aria-label={`${c.label}, collapse`} onClick={() => onToggleGroup(c.expandedFrom!)}>{c.label}</button>
                      : c.label}
                </th>
              )
            })}
            {colsAfter > 0 && <th ref={size('inline-size', colsAfter)} aria-hidden="true" className="version-matrix__gap" />}
          </tr>
        </thead>
        <tbody>
          {rowsBefore > 0 && <tr ref={size('block-size', rowsBefore)} aria-hidden="true" />}
          {rows.map(item => {
            const b = bundles[item.index]
            const st = states[item.index]
            const phase = displayState('bundle', b.phase)
            return (
              <tr key={b.name} className="version-matrix__row" data-bundle={b.name} aria-rowindex={item.index + 2}
                data-followed={b.name === followed || undefined} data-compared={b.name === compared || undefined}>
                <th scope="row" className="version-matrix__version">
                  <button type="button" aria-current={b.name === followed ? 'true' : undefined}
                    onClick={e => (e.shiftKey ? onCompare(b.name) : onSelect(b.name))}>
                    <VersionLabel bundle={b} compact />
                  </button>
                  <StatusChip tone={phase.tone} label={phase.label} glyph={phase.glyph} compact />
                </th>
                {colsBefore > 0 && <td aria-hidden="true" className="version-matrix__gap" />}
                {cols.map(ci => (
                  <Cell key={columns[ci.index].id} column={columns[ci.index]} at={st} r={item.index} c={ci.index}
                    focusable={stop.r === item.index && stop.c === ci.index} />
                ))}
                {colsAfter > 0 && <td aria-hidden="true" className="version-matrix__gap" />}
              </tr>
            )
          })}
          {rowsAfter > 0 && <tr ref={size('block-size', rowsAfter)} aria-hidden="true" />}
        </tbody>
      </table>
    </div>
    <p className="version-matrix__status" aria-hidden="true">{status}</p>
    </div>
  )
}

/** The words of a cell: an environment's state, or a group's live count and most urgent state. */
function cellText(column: Column, at: Map<string, string> | undefined): string {
  const m = at ?? new Map<string, string>()
  if (!column.group) return stateAt(m, column.envs[0]).label
  const g = groupState(column, m)
  return `${g.live} of ${column.envs.length} live${g.worst && g.worst.tone !== 'verified' ? `, ${g.worst.label.toLowerCase()} in some` : ''}`
}

function groupState(column: Column, at: Map<string, string>): { worst?: DisplayState; live: number } {
  let worst: DisplayState | undefined
  let live = 0
  for (const env of column.envs) {
    const d = stateAt(at, env)
    if (d.tone === 'verified') live++
    if (!worst || TONE_ORDER.indexOf(d.tone) < TONE_ORDER.indexOf(worst.tone)) worst = d
  }
  return { worst, live }
}

/** The row and column a focus or key event's cell names. */
function cellOf(t: EventTarget): { r: number; c: number } | undefined {
  const v = (t as HTMLElement).getAttribute?.('data-cell')
  if (!v) return undefined
  const [r, c] = v.split(':').map(Number)
  return { r, c }
}

const Cell = memo(function Cell({ column, at, r, c, focusable }: {
  column: Column; at: Map<string, string>; r: number; c: number; focusable: boolean
}) {
  const common = { role: 'gridcell', tabIndex: focusable ? 0 : -1, 'data-cell': `${r}:${c}`, 'aria-colindex': c + 2 } as const
  if (!column.group) {
    const d = stateAt(at, column.envs[0])
    return (
      <td className="version-matrix__cell" data-tone={d.tone} {...common} aria-label={`${column.label}: ${d.label}`}>
        <span aria-hidden="true">{d.glyph}</span>
      </td>
    )
  }
  // A collapsed wave or fleet: its most urgent state, and how many of its environments are live.
  const g = groupState(column, at)
  return (
    <td className="version-matrix__cell" data-tone={g.worst?.tone} {...common} aria-label={`${column.label}: ${cellText(column, at)}`}>
      <span aria-hidden="true">{g.worst?.glyph}</span>
      <span className="version-matrix__count" aria-hidden="true">{`${g.live}/${column.envs.length}`}</span>
    </td>
  )
})

/** The state of a version in an environment it has (or has not) reached. */
function stateAt(at: Map<string, string>, env: string): DisplayState {
  return displayState('step', at.get(env) || 'NotStarted')
}

/** The matrix's columns: the map's collapsed nodes, an expanded group as its members. */
export function columnsOf(model: StationMapModel): Column[] {
  return model.nodes.flatMap((n): Column[] => {
    if (n.kind === 'station') return [{ id: n.id, label: n.id, envs: [n.id] }]
    if (!n.expanded) return [{ id: n.id, label: `${n.group.label} · ${n.group.members.length}`, envs: n.group.members, group: n.group }]
    return n.group.members.map(m => ({ id: m, label: m, envs: [m], expandedFrom: n.id }))
  })
}
