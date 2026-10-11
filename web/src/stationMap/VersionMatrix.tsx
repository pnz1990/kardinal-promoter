// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stationMap/VersionMatrix.tsx — the pipeline as a matrix (DESIGN-SYSTEM §3.5): one
// row per version (Bundle), newest first, one column per environment, where a cell
// is the version's state there. It answers "where did 1.4.2 reach?" for many
// versions at once. Columns collapse into waves and fleets like the map (a group's
// cell counts its members); rows are virtualized, so 200 versions × 300
// environments scroll as fast as 20.

import { useCallback, useMemo, useRef } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import type { Bundle } from '../types'
import type { Group, StationMapModel } from './model'
import { TONE_ORDER } from './model'
import { displayState, type DisplayState, type Tone } from './display'
import { StatusChip } from '../ui'
import { versionName } from '../versionName'
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

export default function VersionMatrix({ model, pipeline, bundles, followed, compared, onSelect, onCompare, onToggleGroup }: VersionMatrixProps) {
  const scroller = useRef<HTMLDivElement | null>(null)
  const columns = useMemo(() => columnsOf(model), [model])
  const states = useMemo(() => bundles.map(b => new Map((b.environments ?? []).map(e => [e.name, e.phase ?? '']))), [bundles])
  const virtualizer = useVirtualizer({
    count: bundles.length,
    getScrollElement: () => scroller.current,
    estimateSize: () => ROW_H,
    overscan: OVERSCAN,
    // jsdom measures nothing: start from a window's worth of rows.
    initialRect: { width: ROW_H * VIEW_ROWS, height: ROW_H * VIEW_ROWS },
  })
  // Before the scroller is measured (and in jsdom) the virtualizer has no window yet: show the first rows.
  const measured = virtualizer.getVirtualItems()
  const items = measured.length > 0 || bundles.length === 0 ? measured
    : bundles.slice(0, VIEW_ROWS + OVERSCAN).map((_, index) => ({ index, start: index * ROW_H, end: (index + 1) * ROW_H, key: index, size: ROW_H, lane: 0 }))
  const before = items.length ? items[0].start : 0
  const after = items.length ? Math.max(0, bundles.length * ROW_H - items[items.length - 1].end) : 0
  // Spacer heights are computed from the scroll position, so they are set on the element.
  const spacer = useCallback((h: number) => (el: HTMLTableRowElement | null) => { el?.style.setProperty('block-size', `${h}px`) }, [])

  return (
    <div ref={scroller} className="version-matrix" role="region" aria-label="Versions by environment" tabIndex={0}>
      <table className="version-matrix__table">
        <caption className="sr-only">
          Each of the {bundles.length} versions of {pipeline}, newest first, and its state in each environment.
          Select a version to follow it on the map; Shift-select one to compare it with the followed version.
        </caption>
        <thead>
          <tr>
            <th scope="col" className="version-matrix__head version-matrix__corner">Version</th>
            {columns.map(c => (
              <th key={c.id} scope="col" className="version-matrix__head" title={c.group ? `${c.envs.length} environments` : undefined}>
                {c.group
                  ? <button type="button" aria-expanded={false} onClick={() => onToggleGroup(c.group!.id)}>{`${c.label} ▸`}</button>
                  : c.expandedFrom
                    ? <button type="button" aria-expanded={true} title="Collapse" onClick={() => onToggleGroup(c.expandedFrom!)}>{c.label}</button>
                    : c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {before > 0 && <tr ref={spacer(before)} aria-hidden="true" />}
          {items.map(item => {
            const b = bundles[item.index]
            const at = states[item.index]
            const phase = displayState('bundle', b.phase)
            return (
              <tr key={b.name} className="version-matrix__row" data-bundle={b.name}
                data-followed={b.name === followed || undefined} data-compared={b.name === compared || undefined}>
                <th scope="row" className="version-matrix__version">
                  <button type="button" title={b.name} aria-current={b.name === followed ? 'true' : undefined}
                    onClick={e => (e.shiftKey ? onCompare(b.name) : onSelect(b.name))}>
                    {versionName(b.name, pipeline)}
                  </button>
                  <StatusChip tone={phase.tone} label={phase.label} glyph={phase.glyph} compact />
                </th>
                {columns.map(c => <Cell key={c.id} column={c} at={at} />)}
              </tr>
            )
          })}
          {after > 0 && <tr ref={spacer(after)} aria-hidden="true" />}
        </tbody>
      </table>
    </div>
  )
}

function Cell({ column, at }: { column: Column; at: Map<string, string> }) {
  if (!column.group) {
    const d = stateAt(at, column.envs[0])
    return (
      <td className="version-matrix__cell" data-tone={d.tone} title={`${column.label}: ${d.label}`}>
        <span aria-hidden="true">{d.glyph}</span><span className="sr-only">{d.label}</span>
      </td>
    )
  }
  // A collapsed wave or fleet: its most urgent state, and how many of its environments are live.
  const tones = new Map<Tone, number>()
  let worst: DisplayState | undefined
  let live = 0
  for (const env of column.envs) {
    const d = stateAt(at, env)
    tones.set(d.tone, (tones.get(d.tone) ?? 0) + 1)
    if (d.tone === 'verified') live++
    if (!worst || TONE_ORDER.indexOf(d.tone) < TONE_ORDER.indexOf(worst.tone)) worst = d
  }
  const summary = `${live} of ${column.envs.length} live`
  return (
    <td className="version-matrix__cell" data-tone={worst?.tone} title={`${column.label}: ${summary}`}>
      <span aria-hidden="true">{worst?.glyph}</span>
      <span className="version-matrix__count">{`${live}/${column.envs.length}`}</span>
      <span className="sr-only">{`, ${summary}`}</span>
    </td>
  )
}

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
