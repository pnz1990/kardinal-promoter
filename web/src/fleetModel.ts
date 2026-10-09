// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// fleetModel.ts — the fleet board's view of a Pipeline: its environments laid
// out as stations in promotion order, what each runs, and where the active
// Bundle is on its way. Pure functions; FleetBoard draws the result.

import type { Pipeline } from './types'

/** Where the active Bundle is relative to one environment. */
export type StationState =
  | 'settled'   // runs the active Bundle (or nothing is moving)
  | 'arriving'  // the active Bundle is promoting into it now
  | 'failed'    // the active Bundle failed here
  | 'held'      // the active Bundle waits here behind a gate
  | 'ahead'     // the active Bundle has not reached it yet
  | 'empty'     // never deployed and nothing on its way

export interface Station {
  env: string
  /** Version it runs, '' when never deployed. */
  version: string
  /** Bundle it runs, '' when never deployed. */
  bundle: string
  verifiedAt?: string
  /** What else it runs from another Bundle: "config abc1234" under an image
   *  Bundle, the image tags under a config Bundle, as kardinal status says (#1353). */
  alsoRuns?: string
  /** The Bundle alsoRuns comes from. */
  alsoFrom?: string
  state: StationState
  /** The active Bundle's step state here, when it has one. */
  incomingState?: string
}

export interface FleetRow {
  pipeline: Pipeline
  /** Stations grouped by depth: group i holds the environments i steps from a root. */
  groups: Station[][]
  /** Index of the group the active Bundle is moving into, or -1. */
  liveGroup: number
  /** What the live connector shows: moving, held at a gate, or failed. */
  live?: 'arriving' | 'held' | 'failed'
  /** The active Bundle's version, when it is still on its way somewhere. */
  incomingVersion?: string
}

const ARRIVING = new Set(['Promoting', 'WaitingForMerge', 'HealthChecking'])
const FAILED = new Set(['Failed', 'AbortedByAlarm', 'RollingBack'])

/**
 * Environments of a Pipeline grouped by depth (longest path from a root), in
 * spec order. Edges are the controller-resolved upstreams; when the API sends
 * none (an older controller, or an invalid ordering), dependsOn, else the
 * previous entry, as the DAG view does.
 */
export function depthGroups(p: Pipeline): string[][] {
  const topo = p.environmentTopology
  if (!topo || topo.length === 0) {
    const envs = Object.keys(p.environmentStates ?? {})
    return envs.length ? [envs] : []
  }
  const resolved = topo.some(e => (e.upstreams ?? []).length > 0)
  const ups = new Map<string, string[]>(topo.map((e, i) => {
    if (resolved) return [e.name, e.upstreams ?? []]
    if (e.dependsOn && e.dependsOn.length > 0) return [e.name, e.dependsOn]
    return [e.name, i > 0 ? [topo[i - 1].name] : []]
  }))
  const depth = new Map<string, number>()
  const visit = (name: string, seen: Set<string>): number => {
    const known = depth.get(name)
    if (known !== undefined) return known
    if (seen.has(name)) return 0 // a cycle: the controller refuses it; do not loop
    seen.add(name)
    let d = 0
    for (const up of ups.get(name) ?? []) {
      if (ups.has(up)) d = Math.max(d, visit(up, seen) + 1)
    }
    depth.set(name, d)
    return d
  }
  const groups: string[][] = []
  for (const e of topo) {
    const d = visit(e.name, new Set())
    ;(groups[d] ??= []).push(e.name)
  }
  return groups.filter(g => g && g.length > 0)
}

/** The fleet board row of one Pipeline. */
export function fleetRow(p: Pipeline): FleetRow {
  const states = p.environmentStates ?? {}
  const deployed = p.deployed ?? {}
  const active = p.activeBundleName
  const groups = depthGroups(p).map(g => g.map((env): Station => {
    const d = deployed[env]
    const incoming = active ? states[env] : undefined
    let state: StationState
    if (incoming && FAILED.has(incoming)) state = 'failed'
    else if (incoming && ARRIVING.has(incoming)) state = 'arriving'
    else if (incoming === 'Verified' || (d && (!active || d.bundle === active))) state = 'settled'
    // The active Bundle lists only the environments it has reached, so one
    // with no state there is still ahead of it.
    else if (active) state = 'ahead'
    else state = d ? 'settled' : 'empty'
    const alsoRuns = d?.configVersion || d?.imagesVersion || undefined
    const alsoFrom = d?.configVersion ? d.configFrom : d?.imagesVersion ? d.imagesFrom : undefined
    return { env, version: d?.version ?? '', bundle: d?.bundle ?? '', verifiedAt: d?.verifiedAt, alsoRuns, alsoFrom, state, incomingState: incoming }
  }))

  let liveGroup = groups.findIndex(g => g.some(s => s.state === 'failed'))
  let live: FleetRow['live'] = liveGroup >= 0 ? 'failed' : undefined
  if (liveGroup < 0) {
    liveGroup = groups.findIndex(g => g.some(s => s.state === 'arriving'))
    if (liveGroup >= 0) live = 'arriving'
  }
  if (liveGroup < 0 && (p.blockerCount ?? 0) > 0) {
    // Gates hold the Bundle before the first environment it has not reached.
    liveGroup = groups.findIndex(g => g.some(s => s.state === 'ahead'))
    if (liveGroup >= 0) {
      live = 'held'
      for (const s of groups[liveGroup]) if (s.state === 'ahead') s.state = 'held'
    }
  }
  const moving = groups.some(g => g.some(s => s.state !== 'settled' && s.state !== 'empty'))
  return {
    pipeline: p,
    groups,
    liveGroup,
    live,
    incomingVersion: moving ? p.activeBundleVersion || active : undefined,
  }
}

/** "now", "5m", "3h", "2d": how long ago an RFC 3339 time was, coarse. */
export function ageOf(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return ''
  const t = new Date(iso).getTime()
  if (isNaN(t)) return ''
  const min = Math.floor((now - t) / 60000)
  if (min < 1) return 'now'
  if (min < 60) return `${min}m`
  const h = Math.floor(min / 60)
  if (h < 48) return `${h}h`
  return `${Math.floor(h / 24)}d`
}
