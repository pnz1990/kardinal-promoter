// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// fleetModel.ts — the fleet board's view of a Pipeline: its environments laid
// out as stations in promotion order, what each runs, and where the active
// Bundle is on its way. Pure functions; FleetBoard draws the result.

import type { EnvironmentFleet, EnvironmentNode, Pipeline } from './types'

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
  /** Set on a fleet environment: its targets rolled up. */
  fleet?: FleetRollup
}

/** A fleet environment's targets, counted by where the active Bundle is. */
export interface FleetRollup {
  total: number
  verified: number
  /** Started and not yet Verified or Failed. */
  inFlight: number
  failed: number
  /** Not started by the active Bundle. */
  pending: number
  maxConcurrent: number
  maxUnavailable?: number
  /** maxUnavailable targets Failed: no further target starts. */
  stopped: boolean
  /** Distinct versions the targets run: more than one while a rollout is part way. */
  versions: number
  message?: string
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
 * The upstream environments of each environment of a topology. Edges are the
 * controller-resolved upstreams when the API says the ordering is resolved
 * (Pipeline.topologyResolved; then an entry without upstreams is a root, as
 * in a Pipeline whose every environment is in wave 1). Without the flag (an
 * older controller, or an invalid ordering), upstreams when any entry has
 * them, else dependsOn, else the previous entry.
 */
export function environmentUpstreams(topo: EnvironmentNode[], topologyResolved?: boolean): Map<string, string[]> {
  const resolved = topologyResolved || topo.some(e => (e.upstreams ?? []).length > 0)
  return new Map<string, string[]>(topo.map((e, i) => {
    if (resolved) return [e.name, e.upstreams ?? []]
    if (e.dependsOn && e.dependsOn.length > 0) return [e.name, e.dependsOn]
    return [e.name, i > 0 ? [topo[i - 1].name] : []]
  }))
}

/**
 * The environments a release ends in: those no other environment waits for,
 * in spec order. For test, uat, prod that is prod; for a last wave of 149
 * environments it is all 149. Empty without a topology.
 */
export function terminalEnvironments(topo: EnvironmentNode[] | undefined, topologyResolved?: boolean): string[] {
  if (!topo || topo.length === 0) return []
  const ups = environmentUpstreams(topo, topologyResolved)
  const waitedFor = new Set<string>()
  for (const list of ups.values()) for (const up of list) waitedFor.add(up)
  return topo.map(e => e.name).filter(name => !waitedFor.has(name))
}

/**
 * Environments of a Pipeline grouped by depth (longest path from a root), in
 * spec order, with the upstreams of environmentUpstreams.
 */
export function depthGroups(p: Pipeline): string[][] {
  const topo = p.environmentTopology
  if (!topo || topo.length === 0) {
    const envs = Object.keys(p.environmentStates ?? {})
    return envs.length ? [envs] : []
  }
  return groupByDepth(topo.map(e => e.name), environmentUpstreams(topo, p.topologyResolved))
}

/**
 * groupByDepth groups names by their longest path from a root, keeping the
 * order of names inside each group. Upstreams not in names are ignored.
 */
export function groupByDepth(names: string[], ups: Map<string, string[]>): string[][] {
  const known = new Set(names)
  const depth = new Map<string, number>()
  const visit = (name: string, seen: Set<string>): number => {
    const d0 = depth.get(name)
    if (d0 !== undefined) return d0
    if (seen.has(name)) return 0 // a cycle: the controller refuses it; do not loop
    seen.add(name)
    let d = 0
    for (const up of ups.get(name) ?? []) {
      if (known.has(up)) d = Math.max(d, visit(up, seen) + 1)
    }
    depth.set(name, d)
    return d
  }
  const groups: string[][] = []
  for (const name of names) {
    const d = visit(name, new Set())
    ;(groups[d] ??= []).push(name)
  }
  return groups.filter(g => g && g.length > 0)
}

/** One station for a fleet environment: its targets rolled up. */
function fleetStation(env: string, f: EnvironmentFleet, p: Pipeline): Station {
  const states = p.environmentStates ?? {}
  const deployed = p.deployed ?? {}
  const active = p.activeBundleName
  let verified = 0, inFlight = 0, failed = 0, pending = 0
  const versions = new Map<string, number>()
  let latest: string | undefined
  for (const t of f.targets) {
    const st = active ? states[t] : undefined
    if (st === 'Verified') verified++
    else if (st && FAILED.has(st)) failed++
    else if (st) inFlight++ // a step exists: it holds a place until Verified
    else pending++
    const d = deployed[t]
    if (d) {
      const v = d.version || d.bundle
      versions.set(v, (versions.get(v) ?? 0) + 1)
      if (d.verifiedAt && (!latest || d.verifiedAt > latest)) latest = d.verifiedAt
    }
  }
  const total = f.targets.length
  if (!active) {
    // Nothing is moving: every target that runs something is settled.
    verified = 0; pending = 0; inFlight = 0; failed = 0
  }
  const stopped = f.maxUnavailable !== undefined && failed >= f.maxUnavailable
  let state: StationState
  if (failed > 0) state = 'failed'
  else if (active && inFlight > 0) state = 'arriving'
  else if (active && verified === total && total > 0) state = 'settled'
  else if (active && verified > 0) state = 'arriving' // part way: the next targets wait for a place
  else if (active) state = 'ahead'
  else state = versions.size > 0 ? 'settled' : 'empty'
  // The version the most targets run, and how many versions there are.
  let version = ''
  let most = 0
  for (const [v, n] of versions) if (n > most) { version = v; most = n }
  const bundle = [...f.targets].map(t => deployed[t]?.bundle).find(b => b) ?? ''
  return {
    env, version, bundle, verifiedAt: versions.size === 1 ? latest : undefined, state,
    incomingState: failed > 0 ? 'Failed' : inFlight > 0 ? 'Promoting' : undefined,
    fleet: {
      total, verified, inFlight, failed, pending, maxConcurrent: f.maxConcurrent ?? 0,
      maxUnavailable: f.maxUnavailable, stopped, versions: versions.size, message: f.message,
    },
  }
}

/** The fleet board row of one Pipeline. */
export function fleetRow(p: Pipeline): FleetRow {
  const states = p.environmentStates ?? {}
  const deployed = p.deployed ?? {}
  const active = p.activeBundleName
  const fleets = new Map((p.environmentTopology ?? []).filter(e => e.fleet).map(e => [e.name, e.fleet!]))
  const groups = depthGroups(p).map(g => g.map((env): Station => {
    const f = fleets.get(env)
    if (f) return fleetStation(env, f, p)
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

/** This many stations or more at one depth are drawn as one wave plate (and one lane card). */
export const WAVE_PLATE_MIN = 5

/** One plate for a wave of stations at the same depth. */
export interface WavePlate {
  /** First and last environment, in spec order. */
  first: string
  last: string
  size: number
  /** The state that needs attention first: failed, held, arriving, ahead, settled, empty. */
  state: StationState
  /** The version most of the wave runs, '' when none runs one. */
  version: string
  /** Stations that run another version than `version`. */
  otherVersions: number
  /** How many stations are in each state, most urgent first. */
  counts: Array<{ state: StationState; count: number }>
}

const PLATE_ORDER: StationState[] = ['failed', 'held', 'arriving', 'ahead', 'settled', 'empty']

/** The plate of a wave of stations (at least one). */
export function wavePlate(stations: Station[]): WavePlate {
  const byState = new Map<StationState, number>()
  const versions = new Map<string, number>()
  for (const s of stations) {
    byState.set(s.state, (byState.get(s.state) ?? 0) + 1)
    if (s.version) versions.set(s.version, (versions.get(s.version) ?? 0) + 1)
  }
  let version = ''
  let most = 0
  for (const [v, n] of versions) if (n > most) { version = v; most = n }
  const counts = PLATE_ORDER.filter(st => byState.has(st)).map(st => ({ state: st, count: byState.get(st)! }))
  return {
    first: stations[0].env,
    last: stations[stations.length - 1].env,
    size: stations.length,
    state: counts[0].state,
    version,
    otherVersions: stations.length - most - stations.filter(s => !s.version).length,
    counts,
  }
}
