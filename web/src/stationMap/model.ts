// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stationMap/model.ts — the station map's data: one station per environment
// (a fleet target included) with what it runs and what is on its way, the
// gates mounted on the edge into it, the bands and platforms that collapse
// waves and fleets, and the thread, the route of the Bundle being followed.
// Pure functions over the UI API's shapes; StationMap draws the result.
//
// What an environment runs comes from the Pipeline (deployed, the newest step
// that landed its change there), never from the followed Bundle: a prod card
// shows prod's version while a newer one is in uat (UX-AUDIT U2, the
// PipelineLaneView bug). The followed Bundle is the incoming version.

import type { Bundle, EnvironmentHold, GraphNode, GraphResponse, Pipeline, GateApproval, PolicyGate, ActorRef } from '../types'
import { displayState, heldByGate, heldByRollback, type DisplayState, type Tone } from './display'
import { environmentUpstreams, groupByDepth } from '../fleetModel'
import { GRAPH } from './geometry'
import { lowerFirst } from '../sentence'

/** A gate mounted on the edge into the environment it guards. */
export interface GateMark {
  /** Graph node id. */
  id: string
  name: string
  /** graph.GateState: Pass, Block, Waiting, Pending, Superseded, Rejected. */
  state: string
  display: DisplayState
  holding: boolean
  expression?: string
  lastEvaluatedAt?: string
  message?: string
  approval?: GateApproval
}

/** What one environment runs. */
export interface Running {
  bundle: string
  version: string
  verifiedAt?: string
  /** The other half of what it runs: "config abc1234" under an image Bundle (#1353). */
  alsoRuns?: string
  /** Who shipped it (docs/actors.md), when known. */
  actor?: ActorRef
}

/** The followed Bundle in one environment, when it is not what runs there. */
export interface Incoming {
  bundle: string
  version: string
  /** PromotionStep state ("" when the step does not exist yet). */
  state: string
  display: DisplayState
  prURL?: string
  startedAt?: string
  message?: string
  /** Who shipped it (docs/actors.md), when known. */
  actor?: ActorRef
}

export interface Station {
  id: string
  env: string
  /** Longest path from a root. */
  depth: number
  upstreams: string[]
  /** The fleet environment this target belongs to. */
  fleet?: string
  approval?: string
  running?: Running
  incoming?: Incoming
  /** The followed Bundle's state here, or the hold, or not reached. */
  display: DisplayState
  /** The followed Bundle's gates on the edge into this environment. */
  gates: GateMark[]
  hold?: EnvironmentHold
  /** The followed Bundle reached it (a step exists). */
  reached: boolean
  /** The followed Bundle's step here: its graph node id. */
  stepId?: string
}

/** A collapsed set of stations: a wave of one depth (band) or a fleet (platform). */
export interface Group {
  id: string
  kind: 'band' | 'platform'
  /** "WAVE 2", or the fleet environment's name. */
  label: string
  members: string[]
  depth: number
  /** Stations per tone, most urgent first. */
  counts: Array<{ tone: Tone; count: number }>
  /** Stations per state as people read it ("3 failed", "26 not reached"), most urgent first: the
   *  summary's words and the expanded grid's rows. */
  states: Array<{ tone: Tone; label: string; count: number }>
  /** Members that failed. */
  failed: number
  /** The rollout stopped: a fleet's maxUnavailable failed, or every member the
   *  Bundle reached failed. Only then does the thread snap at the entrance. */
  halted: boolean
  /** The version most members run, '' when none runs one. */
  version: string
  /** Members on the followed Bundle that are verified, for the thread's fill. */
  verified: number
  /** fleet.maxConcurrent / maxUnavailable, for a platform's label;
   *  maxUnavailableSpec as written ("10%"). */
  maxConcurrent?: number
  maxUnavailable?: number
  maxUnavailableSpec?: string
  /** The fleet is paused (kardinal pause --env <fleet>): no further target starts. */
  paused?: boolean
}

/** A node of the collapsed graph the layout places (layered.ts). */
export type MapNode =
  | { kind: 'station'; id: string; station: Station }
  | { kind: 'group'; id: string; group: Group; expanded: boolean }

export interface MapEdge {
  from: string
  to: string
}

/** The kind of one segment of the thread: solid into a verified station,
 *  flowing into one in progress, wait (static) into one waiting for a person
 *  (its PR merge), stopped before a gate that holds it, snapped at a failure,
 *  held (ink, not red) into an environment a rollback holds. */
export type ThreadKind = 'solid' | 'flow' | 'wait' | 'stopped' | 'snap' | 'held'

export interface ThreadSegment {
  from: string
  to: string
  kind: ThreadKind
}

export interface StationMapModel {
  stations: Map<string, Station>
  /** Stations in topological order (roving focus, the table and the matrix). */
  order: string[]
  groups: Map<string, Group>
  /** Station id → the group it is in. */
  groupOf: Map<string, string>
  nodes: MapNode[]
  edges: MapEdge[]
  /** The followed Bundle's route over the collapsed graph's edges. */
  thread: ThreadSegment[]
  /** What needs a person, for the attention strip. */
  attention: Attention
}

export interface Attention {
  failed: string[]
  blocked: Array<{ env: string; gate: string }>
  held: string[]
  waiting: string[]
}

/** Tones by urgency: the first one present is a group's colour. */
export const TONE_ORDER: Tone[] = ['failed', 'blocked', 'held', 'promoting', 'waiting', 'verified', 'pending', 'superseded', 'paused', 'degraded', 'unknown']

/** The followed Bundle and its graph. */
export interface Followed {
  bundle: Bundle
  graph?: GraphResponse
}

/** A Bundle's verified actor as the short ref the pipeline list carries. */
function refOfBundle(b?: Bundle): ActorRef | undefined {
  const a = b?.actor
  if (!a || a.kind === 'unknown' || !b?.actorText) return undefined
  return { kind: a.kind, agent: a.agent, sponsor: a.sponsor, text: b.actorText }
}

/** What a Bundle ships, short: its image tag(s), else its config commit, else its name. */
export function bundleVersion(b: Bundle | undefined, fallback = ''): string {
  if (!b) return fallback
  const tags = (b.images ?? []).map(i => i.tag || (i.digest ? i.digest.slice(0, 19) : '')).filter(Boolean)
  if (tags.length > 0) return tags.join(', ')
  return fallback || b.name
}

/** The followed Bundle's steps and gates by environment. */
function indexGraph(graph: GraphResponse | undefined) {
  const steps = new Map<string, GraphNode>()
  const gatesInto = new Map<string, GraphNode[]>()
  if (!graph) return { steps, gatesInto }
  const byId = new Map(graph.nodes.map(n => [n.id, n]))
  for (const n of graph.nodes) {
    if (n.type === 'PromotionStep') steps.set(n.environment, n)
  }
  // A gate guards the environment the UI API names (environment), the step
  // its edge leads into (ui_api.go handleBundleGraph sends both).
  for (const n of graph.nodes) {
    if (n.type !== 'PolicyGate') continue
    const into = graph.edges.map(e => e.from === n.id ? byId.get(e.to) : undefined).find(t => t?.type === 'PromotionStep')
    const env = into?.environment ?? n.environment
    const list = gatesInto.get(env) ?? []
    list.push(n)
    gatesInto.set(env, list)
  }
  return { steps, gatesInto }
}

/** The environments of a Pipeline with fleets expanded into their targets.
 *  Without a topology (an older controller), the followed Bundle's graph: its
 *  steps, with the edges between them (through gates) as upstreams. */
function expandedTopology(p: Pipeline, graph?: GraphResponse) {
  const out: Array<{ name: string; upstreams: string[]; fleet?: string; approval?: string; hold?: EnvironmentHold }> = []
  const topo = p.environmentTopology ?? []
  const ups = environmentUpstreams(topo, p.topologyResolved)
  // An upstream that is a fleet means all of its targets.
  const targetsOf = new Map(topo.filter(e => e.fleet).map(e => [e.name, e.fleet!.targets]))
  const expand = (names: string[]) => names.flatMap(n => targetsOf.get(n) ?? [n])
  for (const e of topo) {
    const upstreams = expand(ups.get(e.name) ?? [])
    if (e.fleet) {
      for (const t of e.fleet.targets) {
        out.push({ name: t, upstreams, fleet: e.name, approval: e.approval, hold: e.hold })
      }
    } else {
      out.push({ name: e.name, upstreams, approval: e.approval, hold: e.hold })
    }
  }
  if (out.length === 0 && graph) {
    const byId = new Map(graph.nodes.map(n => [n.id, n]))
    const ups = new Map<string, Set<string>>()
    // A step's upstream steps: directly, or through the gates in between.
    const stepUps = (id: string, seen = new Set<string>()): string[] => graph.edges
      .filter(e => e.to === id && !seen.has(e.from))
      .flatMap(e => {
        seen.add(e.from)
        const from = byId.get(e.from)
        if (!from) return []
        return from.type === 'PromotionStep' ? [from.environment] : stepUps(from.id, seen)
      })
    for (const n of graph.nodes) {
      if (n.type !== 'PromotionStep') continue
      ups.set(n.environment, new Set(stepUps(n.id)))
    }
    // An environment with gates but no step yet (an older controller sends none) is a station too.
    for (const n of graph.nodes) {
      if (n.type !== 'PolicyGate' || !n.environment || ups.has(n.environment)) continue
      ups.set(n.environment, new Set(stepUps(n.id)))
    }
    for (const [name, u] of ups) out.push({ name, upstreams: [...u] })
  }
  if (out.length === 0) {
    for (const name of Object.keys(p.environmentStates ?? {})) out.push({ name, upstreams: [] })
  }
  return out
}

export interface BuildOptions {
  /** Groups the user expanded (group ids). */
  expanded?: Set<string>
  /** Collapse waves of this many stations or more (graph.waveCollapseAt). */
  waveCollapseAt?: number
  /** The PolicyGates (GET /gates): an approval gate's quorum joins its graph node. */
  gates?: PolicyGate[]
  /** The Pipeline's Bundles: a followed Bundle older than what runs is not incoming. */
  bundles?: Bundle[]
}

/** The PolicyGate behind a graph gate node: the UI API's id is "gate-<PolicyGate name>". */
function gateOf(node: GraphNode, gates: PolicyGate[] | undefined, bundle: Bundle | undefined): PolicyGate | undefined {
  if (!gates || gates.length === 0) return undefined
  return gates.find(g => `gate-${g.name}` === node.id) ??
    gates.find(g => !g.template && g.environment === node.environment && (g.name === node.label || g.name.startsWith(`${node.label}-`)) &&
      (!bundle || !g.bundle || g.bundle === bundle.name))
}

/** A holding approval gate reads as waiting for a person (its state stays Block). */
function waitingForApproval(d: DisplayState): DisplayState {
  return { ...d, label: 'Waiting for approval' }
}

/** Step states the Bundle has finished with: a holding gate no longer decides them. */
const SETTLED = new Set(['Verified', 'Failed', 'AbortedByAlarm', 'RollingBack', 'Superseded'])

/** buildModel builds the station map of p, following followed. */
export function buildModel(p: Pipeline, followed: Followed | undefined, opts: BuildOptions = {}): StationMapModel {
  const collapseAt = opts.waveCollapseAt ?? GRAPH.waveCollapseAt
  const expanded = opts.expanded ?? new Set<string>()
  const topo = expandedTopology(p, followed?.graph)
  const deployed = p.deployed ?? {}
  const { steps, gatesInto } = indexGraph(followed?.graph)
  const followedName = followed?.bundle.name
  const followedVersion = bundleVersion(followed?.bundle,
    followedName && followedName === p.activeBundleName ? p.activeBundleVersion : undefined)

  // Depth over the expanded environments.
  const upsMap = new Map(topo.map(e => [e.name, e.upstreams]))
  const depthGroups = groupByDepth(topo.map(e => e.name), upsMap)
  const depth = new Map<string, number>()
  depthGroups.forEach((g, d) => g.forEach(n => depth.set(n, d)))

  const stations = new Map<string, Station>()
  const attention: Attention = { failed: [], blocked: [], held: [], waiting: [] }
  // P1 (Pipeline status.environments) says what runs where; deployed is the
  // older controllers' source of the same.
  const p1 = new Map((p.environments ?? []).map(e => [e.name, e]))
  for (const e of topo) {
    const d = deployed[e.name]
    const pe = p1.get(e.name)
    const running: Running | undefined = pe?.bundle ? {
      bundle: pe.bundle,
      version: pe.version || pe.bundle,
      verifiedAt: d?.verifiedAt ?? pe.since,
      alsoRuns: d?.configVersion || d?.imagesVersion || undefined,
      actor: d?.bundle === pe.bundle ? d.actor : refOfBundle(opts.bundles?.find(b => b.name === pe.bundle)),
    } : d ? {
      bundle: d.bundle,
      version: d.version || d.bundle,
      verifiedAt: d.verifiedAt,
      alsoRuns: d.configVersion || d.imagesVersion || undefined,
      actor: d.actor,
    } : undefined
    const step = steps.get(e.name)
    const gates: GateMark[] = (gatesInto.get(e.name) ?? []).map(g => ({
      id: g.id, name: g.label, state: g.state, display: displayState('gate', g.state),
      holding: !!g.holding, expression: g.expression, lastEvaluatedAt: g.lastEvaluatedAt, message: g.message,
      approval: gateOf(g, opts.gates, followed?.bundle)?.approval,
    })).map(g => (g.approval && g.holding ? { ...g, display: waitingForApproval(g.display) } : g))
    // Without the followed Bundle's graph, P1 says where the newest Bundle stands.
    // (P1's state is the newest Bundle's: what is incoming, else what runs.)
    const p1State = !step && pe && followedName && (pe.incomingBundle ? pe.incomingBundle === followedName : pe.bundle === followedName) ? pe.state ?? '' : ''
    const stepState = step?.state ?? p1State
    const reached = stepState !== 'NotStarted' && stepState !== ''
    let display: DisplayState
    // A gate that holds the Bundle stops it here whatever its step is doing
    // (a PR may be open when the gate turns): the thread stops at the bar.
    const holding = gates.find(g => g.holding) ??
      (!step && p1State && pe?.blockedBy?.length ? { name: pe.blockedBy[0] } as GateMark : undefined)
    if ((e.hold && e.hold.bundle !== followedName) || (pe?.held && pe.bundle !== followedName && !e.hold)) display = heldByRollback(stepState)
    else if (holding && !SETTLED.has(stepState)) {
      // An approval gate holds it for a person, not for a rule.
      display = holding.approval ? { ...heldByGate(stepState, holding.name), label: `Waiting for approval at ${holding.name}` } : heldByGate(stepState, holding.name)
    }
    else if (followed) display = displayState('step', reached ? stepState : 'NotStarted')
    else display = displayState('step', running ? 'Verified' : 'NotStarted')

    // The followed Bundle is incoming here unless it is what runs here, or it
    // is older than what runs here (following an earlier Bundle).
    const runsFollowed = !!running && running.bundle === followedName && stepState === 'Verified'
    const runningBundle = running && opts.bundles?.find(b => b.name === running.bundle)
    const olderThanRunning = !!running && running.bundle !== followedName && (
      (!!runningBundle?.createdAt && !!followed?.bundle.createdAt && followed.bundle.createdAt < runningBundle.createdAt) ||
      stepState === 'Verified' || stepState === 'Superseded')
    // P1's incoming version names the newest Bundle's own version text.
    const p1Incoming = pe?.incomingBundle && pe.incomingBundle === followedName ? pe.incomingVersion : undefined
    const incoming: Incoming | undefined = followed && !runsFollowed && !olderThanRunning && (reached || holding) ? {
      bundle: followed.bundle.name,
      version: p1Incoming || followedVersion,
      state: stepState,
      display,
      prURL: step?.prURL ?? (pe?.incomingBundle === followedName ? pe?.prURL : undefined),
      startedAt: step?.startedAt,
      message: step?.message,
      actor: refOfBundle(followed.bundle),
    } : undefined

    stations.set(e.name, {
      id: e.name, env: e.name, depth: depth.get(e.name) ?? 0, upstreams: e.upstreams, fleet: e.fleet,
      approval: e.approval, running, incoming, display, gates, hold: e.hold, reached, stepId: step?.id,
    })
    if (display.tone === 'failed') attention.failed.push(e.name)
    else if (display.tone === 'blocked' && holding) attention.blocked.push({ env: e.name, gate: holding.name })
    else if (display.tone === 'held') attention.held.push(e.name)
    else if (display.tone === 'waiting' || gates.some(g => g.approval && !g.holding && g.state !== 'Pass' && g.state !== 'Superseded')) attention.waiting.push(e.name)
  }
  const order = depthGroups.flat()

  // Groups: each fleet is a platform; a depth of collapseAt or more stations
  // (not fleet targets) is a band.
  const groups = new Map<string, Group>()
  const groupOf = new Map<string, string>()
  for (const e of p.environmentTopology ?? []) {
    if (!e.fleet) continue
    const id = `platform:${e.name}`
    groups.set(id, { ...summarize(id, 'platform', e.name, e.fleet.targets.filter(t => stations.has(t)), stations, followedName,
      e.fleet.maxConcurrent, e.fleet.maxUnavailable), maxUnavailableSpec: e.fleet.maxUnavailableSpec, paused: e.fleet.paused || undefined })
    for (const t of e.fleet.targets) groupOf.set(t, id)
  }
  // Bands are numbered in order: WAVE 1 is the first band from the roots.
  let band = 0
  depthGroups.forEach((g, d) => {
    const plain = g.filter(n => !groupOf.has(n))
    if (plain.length < collapseAt) return
    const id = `band:${d}`
    band++
    groups.set(id, summarize(id, 'band', `WAVE ${band}`, plain, stations, followedName))
    for (const n of plain) groupOf.set(n, id)
  })

  // The collapsed graph: a group is one layout node, drawn as its summary,
  // or as its grid once expanded (the layout gets the grid's size).
  const layoutId = (env: string) => groupOf.get(env) ?? env
  const nodes: MapNode[] = []
  const seen = new Set<string>()
  for (const env of order) {
    const id = layoutId(env)
    if (seen.has(id)) continue
    seen.add(id)
    if (id === env) nodes.push({ kind: 'station', id, station: stations.get(env)! })
    else nodes.push({ kind: 'group', id, group: groups.get(id)!, expanded: expanded.has(id) })
  }

  const edgeSet = new Set<string>()
  const edges: MapEdge[] = []
  for (const st of stations.values()) {
    for (const u of st.upstreams) {
      const from = layoutId(u)
      const to = layoutId(st.env)
      if (from === to) continue
      const key = `${from}→${to}`
      if (edgeSet.has(key)) continue
      edgeSet.add(key)
      edges.push({ from, to })
    }
  }

  return {
    stations, order, groups, groupOf, nodes, edges,
    thread: followed ? threadOf(edges, stations, groups) : [],
    attention,
  }
}

function summarize(id: string, kind: Group['kind'], label: string, members: string[], stations: Map<string, Station>,
  followedName: string | undefined, maxConcurrent?: number, maxUnavailable?: number): Group {
  const byTone = new Map<Tone, number>()
  const byLabel = new Map<string, { tone: Tone; label: string; count: number }>()
  const versions = new Map<string, number>()
  let verified = 0
  let reached = 0
  for (const m of members) {
    const st = stations.get(m)!
    byTone.set(st.display.tone, (byTone.get(st.display.tone) ?? 0) + 1)
    const lk = `${st.display.tone}/${st.display.label}`
    const prev = byLabel.get(lk)
    if (prev) prev.count++
    else byLabel.set(lk, { tone: st.display.tone, label: st.display.label, count: 1 })
    if (st.reached) reached++
    if (st.running) versions.set(st.running.version, (versions.get(st.running.version) ?? 0) + 1)
    if (followedName && st.reached && st.display.tone === 'verified') verified++
  }
  let version = ''
  let most = 0
  for (const [v, n] of versions) if (n > most) { version = v; most = n }
  const depth = Math.min(...members.map(m => stations.get(m)!.depth))
  const failed = byTone.get('failed') ?? 0
  const rank = new Map(TONE_ORDER.map((t, i) => [t, i]))
  return {
    id, kind, label, members, depth, version, verified, maxConcurrent, maxUnavailable, failed,
    halted: failed > 0 && (maxUnavailable ? failed >= maxUnavailable : failed >= reached),
    counts: TONE_ORDER.filter(t => byTone.has(t)).map(t => ({ tone: t, count: byTone.get(t)! })),
    states: [...byLabel.values()].sort((a, b) => (rank.get(a.tone)! - rank.get(b.tone)!) || a.label.localeCompare(b.label)),
  }
}

/** The tone of a layout node for the thread: a station's, or a group's most urgent. */
function nodeTone(id: string, stations: Map<string, Station>, groups: Map<string, Group>): { tone: Tone; reached: boolean; done: boolean } {
  const st = stations.get(id)
  if (st) return { tone: st.display.tone, reached: st.reached, done: st.reached && st.display.tone === 'verified' }
  const g = groups.get(id)!
  const reached = g.members.some(m => stations.get(m)!.reached)
  const done = g.verified === g.members.length
  // A group snaps only when its rollout stopped; a minority of failures is a
  // mark on the band while the thread fills by the share that is verified.
  if (g.halted) return { tone: 'failed', reached, done }
  const has = (t: Tone) => g.counts.some(c => c.tone === t)
  if (has('blocked') && !g.members.some(m => { const t = stations.get(m)!.display.tone; return t === 'promoting' || t === 'verified' || t === 'failed' })) {
    return { tone: 'blocked', reached, done }
  }
  if (has('promoting') || (has('failed') && reached)) return { tone: 'promoting', reached, done }
  if (has('waiting')) return { tone: 'waiting', reached, done }
  return { tone: done ? 'verified' : reached ? 'promoting' : 'pending', reached, done }
}

/**
 * threadOf is the followed Bundle's route over the collapsed graph: an edge
 * whose upstream the Bundle has verified carries the thread into its
 * downstream — solid into a verified one, flowing into one in progress or
 * waiting, stopped before a gate that blocks it, snapped at a failure.
 * Nothing is drawn past a station the Bundle has not verified.
 */
export function threadOf(edges: MapEdge[], stations: Map<string, Station>, groups: Map<string, Group>): ThreadSegment[] {
  const out: ThreadSegment[] = []
  for (const e of edges) {
    const from = nodeTone(e.from, stations, groups)
    const to = nodeTone(e.to, stations, groups)
    if (!from.done) continue
    if (to.tone === 'held') out.push({ ...e, kind: 'held' })
    else if (to.tone === 'blocked') out.push({ ...e, kind: 'stopped' })
    else if (!to.reached) continue
    else if (to.tone === 'failed') out.push({ ...e, kind: 'snap' })
    else if (to.tone === 'verified' && to.done) out.push({ ...e, kind: 'solid' })
    else if (to.tone === 'superseded') continue
    else if (to.tone === 'waiting') out.push({ ...e, kind: 'wait' })
    else out.push({ ...e, kind: 'flow' })
  }
  return out
}

/** The environments of a band or platform, sorted for its grid: most urgent
 *  state first (the order of group.states), then in spec order. */
export function gridOrder(g: Group, stations: Map<string, Station>): string[] {
  const rank = new Map(g.states.map((s, i) => [`${s.tone}/${s.label}`, i]))
  const spec = new Map(g.members.map((m, i) => [m, i]))
  const key = (env: string) => { const d = stations.get(env)!.display; return rank.get(`${d.tone}/${d.label}`) ?? 99 }
  return [...g.members].sort((a, b) => (key(a) - key(b)) || (spec.get(a)! - spec.get(b)!))
}

/** "3 failed", "26 not reached": a state count as words. */
export function stateCount(s: { label: string; count: number }): string {
  return `${s.count} ${lowerFirst(s.label)}`
}

/** A group's summary in words: "1 failed, 5 live". */
export function groupSummary(g: Group): string {
  return g.states.map(stateCount).join(', ')
}


/** The station's accessible name: "uat — Live, runs 1.4.2, 3h ago, 1 of 1 gate passing". */
export function stationLabel(st: Station, age: (iso?: string) => string): string {
  const parts = [st.display.label]
  if (st.running) {
    const a = age(st.running.verifiedAt)
    parts.push(`runs ${st.running.version}${a ? `, ${a} ago` : ''}`)
  } else parts.push('nothing deployed')
  if (st.running?.actor) parts.push(`shipped by ${st.running.actor.text}`)
  if (st.incoming && st.incoming.version !== st.running?.version) parts.push(`incoming ${st.incoming.version}${st.incoming.actor ? `, shipped by ${st.incoming.actor.text}` : ''}`)
  if (st.gates.length > 0) {
    const pass = st.gates.filter(g => g.state === 'Pass').length
    const block = st.gates.filter(g => g.holding)
    if (block.length > 0) parts.push(`blocked by ${block.map(g => g.name).join(', ')}`)
    else parts.push(`${pass} of ${st.gates.length} gate${st.gates.length === 1 ? '' : 's'} passing`)
    for (const g of st.gates) {
      if (g.approval && g.state !== 'Pass' && g.state !== 'Superseded') parts.push(`${g.name} has ${g.approval.approved} of ${g.approval.required} approvals`)
    }
  }
  return `${st.env} — ${parts.join(', ')}`
}

/** A gate's rank on an edge: the ones holding the version first, passing ones last. */
const gateRank = (g: GateMark) => (g.holding ? 0 : g.display.tone === 'waiting' ? 1 : g.state === 'Pass' ? 3 : 2)

/** The gates on the edge into a node: a station's, or a group's worst per name. */

export function gatesOn(node: MapNode, model: StationMapModel): GateMark[] {
  // The gate that holds the version comes first, so it is the one the edge names.
  if (node.kind === 'station') return [...node.station.gates].sort((a, b) => gateRank(a) - gateRank(b))
  const byName = new Map<string, GateMark>()
  const rank = gateRank
  for (const m of node.group.members) {
    for (const g of model.stations.get(m)!.gates) {
      const prev = byName.get(g.name)
      if (!prev || rank(g) < rank(prev)) byName.set(g.name, g)
    }
  }
  return [...byName.values()].sort((a, b) => rank(a) - rank(b))
}

/**
 * The map label of an edge's gates, kept short: the first gate (the one holding, by gatesOn's
 * order) by name, after its state glyph when it holds, and how many more: "▮ uat-soak +1",
 * "freeze". The words are in edgeGateSummary.
 */
export function edgeGateLabel(gates: GateMark[]): string {
  const [first, ...rest] = gates
  if (!first) return ''
  const glyph = first.holding && first.display.glyph ? `${first.display.glyph} ` : ''
  return `${glyph}${first.name}${rest.length ? ` +${rest.length}` : ''}`
}

/**
 * An edge's gates in words, for the gate's accessible name and popover: the first by name, with
 * what it does when it holds, then how many more and what they do:
 * "prod-approval (waiting) · +1 passing", "uat-soak (blocking) · +2 more", "freeze".
 */
export function edgeGateSummary(gates: GateMark[]): string {
  const [first, ...rest] = gates
  if (!first) return ''
  const doing = first.holding ? ` (${first.approval ? 'waiting' : 'blocking'})` : ''
  if (rest.length === 0) return `${first.name}${doing}`
  const more = rest.every(g => g.state === 'Pass') ? 'passing' : 'more'
  return `${first.name}${doing} · +${rest.length} ${more}`
}
