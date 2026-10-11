// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stationMap/StageTable.tsx — the station map as a table (DESIGN-SYSTEM §3.5):
// one row per environment, fleet targets included, with its state, what it runs,
// what is on its way, its gates and PR, and its actions. It is the map's linear
// equivalent (for screen readers and narrow screens), sortable by environment
// (spec order), state (most urgent first) and age.

import { useMemo, useState } from 'react'
import type { StationMapModel, Station } from './model'
import { TONE_ORDER } from './model'
import { StatusChip } from '../ui'
import { ageOf } from '../fleetModel'
import { prNumberFromURL } from '../prLink'
import '../styles/StageTable.css'

type SortKey = 'env' | 'state' | 'age'
type Dir = 'ascending' | 'descending'

export interface StageTableProps {
  model: StationMapModel
  /** The environment whose stage drawer is open. */
  focused?: string
  onOpenStage: (env: string) => void
  /** The actions of an environment (Promote, Roll back, Release hold). */
  actionsFor?: (env: string) => React.ReactNode
}

const COLUMNS: Array<{ key: SortKey | undefined; label: string }> = [
  { key: 'env', label: 'Environment' },
  { key: 'state', label: 'State' },
  { key: 'age', label: 'Runs' },
  { key: undefined, label: 'On its way' },
  { key: undefined, label: 'Gates' },
  { key: undefined, label: 'Actions' },
]

export default function StageTable({ model, focused, onOpenStage, actionsFor }: StageTableProps) {
  const [sort, setSort] = useState<{ key: SortKey; dir: Dir }>({ key: 'env', dir: 'ascending' })
  const rows = useMemo(() => sortStations(model, sort.key, sort.dir), [model, sort])
  const by = (key: SortKey) => setSort(s => ({ key, dir: s.key === key && s.dir === 'ascending' ? 'descending' : 'ascending' }))

  return (
    <div className="stage-table" role="region" aria-label="Environments" tabIndex={0}>
      <table>
        <caption className="sr-only">
          The pipeline&apos;s {model.stations.size} environments: what each runs, what is on its way, and what holds it.
        </caption>
        <thead>
          <tr>
            {COLUMNS.map(c => (
              <th key={c.label} scope="col" aria-sort={c.key && sort.key === c.key ? sort.dir : undefined}>
                {c.key
                  ? <button type="button" className="stage-table__sort" onClick={() => by(c.key!)}>{c.label}</button>
                  : c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map(st => <Row key={st.env} st={st} model={model} focused={focused === st.env} onOpenStage={onOpenStage} actionsFor={actionsFor} />)}
        </tbody>
      </table>
    </div>
  )
}

function Row({ st, model, focused, onOpenStage, actionsFor }: {
  st: Station; model: StationMapModel; focused: boolean
  onOpenStage: (env: string) => void; actionsFor?: (env: string) => React.ReactNode
}) {
  const age = ageOf(st.running?.verifiedAt)
  const pr = st.incoming?.prURL ? prNumberFromURL(st.incoming.prURL) : null
  const group = model.groupOf.get(st.env)
  const groupLabel = group ? model.groups.get(group)?.label : undefined
  return (
    <tr data-station={st.env} data-tone={st.display.tone} aria-current={focused ? 'true' : undefined}>
      <th scope="row">
        <button type="button" className="stage-table__env" onClick={() => onOpenStage(st.env)}>{st.env}</button>
        {groupLabel && <span className="stage-table__group">{groupLabel}</span>}
      </th>
      <td><StatusChip tone={st.display.tone} label={st.display.label} glyph={st.display.glyph} compact /></td>
      <td className="stage-table__mono">
        {st.running ? <>{st.running.version}{age && <span className="stage-table__quiet">{` · ${age}`}</span>}</> : <span className="stage-table__quiet">nothing deployed</span>}
      </td>
      <td className="stage-table__mono">
        {st.incoming ? <>{`→ ${st.incoming.version}`}{pr && <>{' · '}<a href={st.incoming.prURL} target="_blank" rel="noreferrer">{`PR ${pr}`}</a></>}</> : ''}
      </td>
      <td>
        {st.gates.length > 0 && (
          <ul className="stage-table__gates">
            {st.gates.map(g => (
              <li key={g.id}>
                <StatusChip tone={g.display.tone} label={`${g.name}: ${g.display.label}`} glyph={g.display.glyph} compact />
                {g.approval && <span className="stage-table__quiet">{` ${g.approval.approved}/${g.approval.required}`}</span>}
              </li>
            ))}
          </ul>
        )}
      </td>
      <td className="stage-table__actions">{actionsFor?.(st.env)}</td>
    </tr>
  )
}

/** The stations of the map in the order a sort asks for; spec order breaks ties. */
export function sortStations(model: StationMapModel, key: SortKey, dir: Dir): Station[] {
  const spec = new Map(model.order.map((env, i) => [env, i]))
  const rank = new Map(TONE_ORDER.map((t, i) => [t, i]))
  const time = (st: Station) => (st.running?.verifiedAt ? Date.parse(st.running.verifiedAt) : -Infinity)
  const cmp = (a: Station, b: Station): number => {
    if (key === 'state') return (rank.get(a.display.tone) ?? 99) - (rank.get(b.display.tone) ?? 99)
    // Age: the most recently verified first.
    if (key === 'age') return time(b) - time(a)
    return 0
  }
  const sign = dir === 'ascending' ? 1 : -1
  return model.order.map(env => model.stations.get(env)!)
    .sort((a, b) => (sign * cmp(a, b)) || (sign * (spec.get(a.env)! - spec.get(b.env)!)))
}
