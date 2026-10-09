// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// FleetBoard.tsx — every Pipeline on one screen, drawn as a line of stations:
// each environment shows the version it runs, and the rail lights up where the
// active Bundle is on its way (moving, held by a gate, or failed). Shown when
// no Pipeline is selected. The data comes from GET /api/v1/ui/pipelines
// (deployed, activeBundleVersion, environmentStates, environmentTopology).

import { Fragment } from 'react'
import '../styles/FleetBoard.css'
import type { Pipeline } from '../types'
import { ageOf, fleetRow, type FleetRow, type Station } from '../fleetModel'

interface FleetBoardProps {
  pipelines: Pipeline[]
  /** Total before the sidebar filter, for the "showing N of M" note. */
  total: number
  onSelect: (name: string, namespace: string) => void
  now?: number
}

/** What a fleet station says: how far the rollout is. */
function fleetNote(s: Station): string {
  const f = s.fleet!
  if (f.message) return f.message
  if (f.stopped) return `stopped: ${f.failed} failed (max ${f.maxUnavailable})`
  if (s.state === 'settled' || s.state === 'empty') {
    if (f.versions > 1) return `${f.total} targets, ${f.versions} versions`
    return s.state === 'empty' ? `${f.total} targets, nothing deployed` : `${f.total} targets`
  }
  if (s.state === 'held') return `held by a gate, ${f.total} targets`
  if (s.state === 'ahead') return `${f.total} targets, not reached yet`
  const parts = [`${f.verified}/${f.total} verified`]
  if (f.inFlight > 0) parts.push(`${f.inFlight} in flight${f.maxConcurrent > 0 ? ` (max ${f.maxConcurrent})` : ''}`)
  if (f.failed > 0) parts.push(`${f.failed} failed`)
  return parts.join(', ')
}

/** What a station says under its version. */
function stationNote(s: Station, now: number): string {
  if (s.fleet) return fleetNote(s)
  switch (s.state) {
    case 'arriving':
      if (s.incomingState === 'WaitingForMerge') return 'waiting for merge'
      if (s.incomingState === 'HealthChecking') return 'checking health'
      return 'promoting'
    case 'failed':
      return s.incomingState === 'RollingBack' ? 'rolling back' : 'release failed'
    case 'held':
      return 'held by a gate'
    case 'empty':
      return 'nothing deployed'
  }
  if (!s.bundle) return 'not reached yet'
  if (!s.verifiedAt) return 'checking health'
  const age = ageOf(s.verifiedAt, now)
  return age === 'now' ? 'verified just now' : `verified ${age} ago`
}

const LIVE_LABEL = { arriving: 'on its way', held: 'held', failed: 'failed' } as const

function Rail({ row, index }: { row: FleetRow; index: number }) {
  const live = row.liveGroup === index ? row.live : undefined
  return (
    <div className="fleet-rail" data-live={live ?? 'none'} aria-hidden={live ? undefined : true}>
      <span className="fleet-rail__line" />
      {live && row.incomingVersion && (
        <span className="fleet-rail__capsule" title={`${row.pipeline.activeBundleName ?? ''}: ${LIVE_LABEL[live]}`}>
          <span className="fleet-rail__version">{row.incomingVersion}</span>
          <span className="sr-only"> {LIVE_LABEL[live]}</span>
        </span>
      )}
    </div>
  )
}

/** A fleet's targets as one bar: Verified, in flight, Failed, still to go. */
function FleetBar({ f }: { f: NonNullable<Station['fleet']> }) {
  const pct = (n: number) => `${(100 * n) / f.total}%`
  return (
    <span className="fleet-station__bar" aria-hidden="true">
      <span data-part="verified" style={{ width: pct(f.verified) }} />
      <span data-part="inflight" style={{ width: pct(f.inFlight) }} />
      <span data-part="failed" style={{ width: pct(f.failed) }} />
    </span>
  )
}

function StationPlate({ s, pipeline, now, onSelect }: {
  s: Station; pipeline: Pipeline; now: number; onSelect: FleetBoardProps['onSelect']
}) {
  return (
    <button
      type="button"
      className="fleet-station"
      data-state={s.state}
      data-fleet={s.fleet ? true : undefined}
      onClick={() => onSelect(pipeline.name, pipeline.namespace)}
      aria-label={`${pipeline.name} ${s.env}: ${s.version ? `${s.version}, ` : ''}${s.alsoRuns ? `with ${s.alsoRuns} from ${s.alsoFrom}, ` : ''}${stationNote(s, now)}`}
    >
      <span className="fleet-station__env">{s.env}</span>
      <span className="fleet-station__version" title={s.bundle || undefined}>{s.version || s.bundle || '—'}</span>
      {s.alsoRuns && (
        <span className="fleet-station__also" title={`from ${s.alsoFrom}`}>+ {s.alsoRuns}</span>
      )}
      {s.fleet && s.fleet.total > 0 && <FleetBar f={s.fleet} />}
      <span className="fleet-station__note">{stationNote(s, now)}</span>
    </button>
  )
}

function FleetLine({ row, now, onSelect }: { row: FleetRow; now: number; onSelect: FleetBoardProps['onSelect'] }) {
  const p = row.pipeline
  const blockers = p.blockerCount ?? 0
  const failed = p.failedStepCount ?? 0
  return (
    <li className="fleet-line" data-live={row.live ?? 'none'}>
      <div className="fleet-line__head">
        <button type="button" className="fleet-line__name" onClick={() => onSelect(p.name, p.namespace)}>
          {p.name}
        </button>
        <span className="fleet-line__ns">{p.namespace}</span>
        <span className="fleet-line__flags">
          {p.paused && <span className="fleet-flag" data-kind="paused">paused</span>}
          {blockers > 0 && <span className="fleet-flag" data-kind="held">{blockers} {blockers === 1 ? 'gate' : 'gates'} holding</span>}
          {failed > 0 && <span className="fleet-flag" data-kind="failed">{failed} failed</span>}
        </span>
      </div>
      <div className="fleet-line__track">
        {row.groups.length === 0 ? (
          <span className="fleet-line__none">No environments</span>
        ) : (
          row.groups.map((g, i) => (
            <Fragment key={g.map(s => s.env).join(',')}>
              {(i > 0 || row.liveGroup === 0) && <Rail row={row} index={i} />}
              <div className="fleet-stop" data-fan={g.length > 1 || undefined} data-live={row.liveGroup === i && row.live ? row.live : undefined}>
                {g.map(s => <StationPlate key={s.env} s={s} pipeline={p} now={now} onSelect={onSelect} />)}
              </div>
            </Fragment>
          ))
        )}
      </div>
    </li>
  )
}

/** The fleet board: one line of stations per Pipeline. */
export function FleetBoard({ pipelines, total, onSelect, now = Date.now() }: FleetBoardProps) {
  // The sidebar's order: namespace, then name. Rows do not jump as releases move.
  const rows = [...pipelines]
    .sort((a, b) => a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name))
    .map(fleetRow)
  const moving = rows.filter(r => r.live === 'arriving').length
  const attention = rows.filter(r => r.live === 'held' || r.live === 'failed').length
  return (
    <section className="fleet-board" aria-labelledby="fleet-title">
      <header className="fleet-board__head">
        <h2 id="fleet-title" className="fleet-board__title">Fleet</h2>
        <p className="fleet-board__summary">
          <span>{pipelines.length === total ? `${total} pipelines` : `${pipelines.length} of ${total} pipelines`}</span>
          <span data-kind="moving" data-zero={moving === 0 || undefined}>{moving} moving</span>
          <span data-kind="attention" data-zero={attention === 0 || undefined}>{attention} {attention === 1 ? 'needs' : 'need'} attention</span>
        </p>
        <p className="fleet-board__hint">The version each environment runs. A lit rail is a release on its way; select a pipeline for its promotion graph.</p>
      </header>
      {rows.length === 0 ? (
        <p className="fleet-board__empty">No pipeline matches this filter. Choose All in the sidebar to see every pipeline.</p>
      ) : (
        <ol className="fleet-board__lines">
          {rows.map(r => (
            <FleetLine key={`${r.pipeline.namespace}/${r.pipeline.name}`} row={r} now={now} onSelect={onSelect} />
          ))}
        </ol>
      )}
    </section>
  )
}
