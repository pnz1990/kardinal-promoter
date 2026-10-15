// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// stage/stageModel.ts — what the stage drawer says about one environment:
// what runs there, what is on its way in, why it is waiting (first), and
// which actions make sense now. Pure functions over the UI API's data, so
// the drawer and its tests read the same answer. State words come from the
// display-state table (displayStates.gen.ts), never from raw API values.

import { displayState, held as heldState, heldByGate, type DisplayState } from '../displayStates.gen'
import { verifyState } from '../components/verifyModel'
import type { Bundle, EnvironmentNode, FailedVersion, GraphEdge, GraphNode, Pipeline, PolicyGate, PromotionStep } from '../types'

/** One reason the environment is not moving, most urgent first. */
export interface WaitReason {
  kind: 'paused' | 'frozen' | 'held' | 'gate' | 'approval' | 'verify' | 'pr' | 'upstream' | 'failed'
  /** One sentence a person can act on. */
  title: string
  /** The sentence's tooltip: the Bundle name behind a version it names. */
  tip?: string
  /** The gate's or step's own message, when there is one. */
  detail?: string
  /** RFC 3339: when the gate was last evaluated (gates only). */
  evaluatedAt?: string
  /** The action that resolves it, when the drawer offers one. */
  next?: StageActionKind
  /** The gate behind a gate, approval or verify reason. */
  gate?: PolicyGate
  /** The PR behind a pr reason. */
  prURL?: string
}

export type StageActionKind =
  | 'approve' | 'reject' | 'override' | 'promote' | 'rollback' | 'release' | 'pause' | 'resume' | 'rejectVersion' | 'openPR'
  // A fleet's pause and resume (spec.pausedFleets, #1835), and the rollback
  // of every target of the fleet a target belongs to.
  | 'pauseFleet' | 'resumeFleet' | 'rollbackFleet'

/** A version the environment can be rolled back to. */
export interface RollbackChoice {
  bundle: string
  version: string
  /** RFC 3339: when it was Verified there. */
  verifiedAt?: string
}

/** The name to show for a gate: the one its user gave it, not the generated
 *  instance name (that stays under Details). */
export function gateLabel(g: PolicyGate): string {
  return g.gateTemplate || g.name
}

/** Whether g is an approval gate still collecting its quorum (no counted reject). */
export function awaitingQuorum(g: PolicyGate): boolean {
  return !!g.approval && !g.approval.rejected && g.approval.approved < g.approval.required
}

/** The prefix of the message a freeze writes on the step it holds
 *  (pkg/reconciler/changewindow FrozenMessage, docs/agentic/freezes.md). */
const FROZEN_PREFIX = 'frozen by '

/** Whether a step's message is a freeze's hold. */
function isFrozenMessage(message: string): boolean {
  return message.startsWith(FROZEN_PREFIX)
}

/** Step states a freeze no longer holds. */
const TERMINAL_STEP = new Set(['Verified', 'Failed', 'AbortedByAlarm', 'Superseded'])

/** Whether g is a verify gate still collecting its verdicts (not failed). */
export function awaitingVerdicts(g: PolicyGate): boolean {
  return !!g.verify && verifyState(g.verify) === 'waiting'
}

/** The state to show for a gate: an approval gate collecting its quorum, or a
 *  verify gate its verdicts, is waiting on verifiers, not blocked by a rule. */
export function gateDisplay(g: PolicyGate): DisplayState {
  if (g.holding && awaitingQuorum(g)) return { ...displayState('gate', 'Waiting'), label: 'Waiting for approval' }
  if (g.holding && awaitingVerdicts(g)) return { ...displayState('gate', 'Waiting'), label: 'Waiting for verdicts' }
  return displayState('gate', g.state)
}

export interface StageView {
  env: string
  /** The fleet this environment is a target of, or its own spec entry. */
  spec?: EnvironmentNode
  fleetOf?: EnvironmentNode

  /** What the environment runs now. */
  running?: { bundle: string; version: string; verifiedAt?: string }
  /** The newest Bundle on its way in, when it is not the one running. */
  incoming?: { bundle: string; version: string }
  /** The state to show for the environment. */
  state: DisplayState
  /** The newest step there (of the incoming Bundle, else of the running one). */
  step?: PromotionStep
  why: WaitReason[]
  /** Blocking and passing gates of the incoming Bundle in this environment. */
  gates: PolicyGate[]
  actions: Set<StageActionKind>
  /** The one action the state calls for, when there is one. */
  primary?: StageActionKind
  rollbackChoices: RollbackChoice[]
}

export interface StageInput {
  env: string
  pipeline: Pipeline
  bundles: Bundle[]
  /** Steps of the Bundle on screen (the active one). */
  steps: PromotionStep[]
  gates: PolicyGate[]
  graph?: { nodes: GraphNode[]; edges: GraphEdge[] }
}

/** The spec entry of env: its own, or the fleet it is a target of. */
export function stageSpec(p: Pipeline, env: string): { spec?: EnvironmentNode; fleetOf?: EnvironmentNode } {
  for (const n of p.environmentTopology ?? []) {
    if (n.name === env) return { spec: n }
    if (n.fleet?.targets.includes(env)) return { spec: n, fleetOf: n }
  }
  return {}
}

/** Bundles that were Verified in env, newest first, other than the one running. */
export function rollbackChoices(bundles: Bundle[], env: string, running?: string): RollbackChoice[] {
  const out: RollbackChoice[] = []
  for (const b of [...bundles].sort((a, z) => (z.createdAt ?? '').localeCompare(a.createdAt ?? ''))) {
    if (b.name === running || b.rejected) continue
    const e = b.environments?.find(x => x.name === env)
    if (e?.phase !== 'Verified') continue
    out.push({ bundle: b.name, version: bundleVersion(b), verifiedAt: e.healthCheckedAt })
  }
  return out
}

/**
 * Why a promotion failed, in the controller's words: the step's message, else the message
 * of the sub-step that failed, else its Ready condition's. Undefined when none was recorded.
 */
export function failureMessage(step: PromotionStep | undefined): string | undefined {
  if (!step) return undefined
  return step.message
    || step.steps?.find(s => s.state === 'Failed')?.message
    || step.conditions?.find(c => c.type === 'Ready' && c.status === 'False')?.message
    || undefined
}

/** One sentence for a failed newest version: "The newest version 1.5.0 (app-b) failed before it started (GraphRejected)." */
export function failedVersionTitle(f: FailedVersion): string {
  const label = f.version && f.version !== f.bundle ? `${f.version} (${f.bundle})` : f.bundle
  return `The newest version ${label} ${f.environment ? `failed in ${f.environment}` : 'failed before it started'} (${f.reason}).`
}

/** The version a Bundle ships: status.version, else its image tag(s), else its name. */
export function bundleVersion(b: Bundle): string {
  if (b.version) return b.version
  const tags = (b.images ?? []).map(i => i.tag || i.digest?.slice(0, 19) || i.repository).filter(Boolean)
  return tags.length > 0 ? tags.join(', ') : b.name
}

const PRIMARY: StageActionKind[] = ['approve', 'release', 'resume', 'promote', 'openPR']
const PROMOTABLE = new Set(['NotStarted', 'Failed', 'AbortedByAlarm'])
/** Step states in which a promotion is running. */
export const IN_FLIGHT: ReadonlySet<string> = new Set(['Promoting', 'WaitingForMerge', 'HealthChecking', 'Verifying'])

/** Everything the drawer shows for env. */
export function stageView(input: StageInput): StageView {
  const { env, pipeline: p, bundles, steps, gates } = input
  const { spec, fleetOf } = stageSpec(p, env)
  const deployed = p.deployed?.[env]
  const deployedBundle = deployed ? bundles.find(b => b.name === deployed.bundle) : undefined
  const running = deployed ? { bundle: deployed.bundle, version: deployed.version || (deployedBundle ? bundleVersion(deployedBundle) : deployed.bundle),
    verifiedAt: deployed.verifiedAt } : undefined
  const active = bundles.find(b => b.name === p.activeBundleName)
  const incoming = active && active.name !== running?.bundle && active.phase !== 'Rejected' && active.phase !== 'Superseded'
    ? { bundle: active.name, version: bundleVersion(active) } : undefined
  const step = steps.find(s => s.environment === env && s.bundle === (incoming?.bundle ?? running?.bundle))
    ?? steps.find(s => s.environment === env)
  const stepState = step?.state ?? p.environmentStates?.[env] ?? (running ? 'Verified' : 'NotStarted')

  const envGates = gates.filter(g => !g.template && g.environment === env && (!incoming || g.bundle === incoming.bundle)
    && (!g.pipeline || g.pipeline === p.name))
  const blocking = envGates.filter(g => g.holding)
  const hold = (fleetOf ? p.environmentTopology?.find(n => n.name === env)?.hold : undefined) ?? spec?.hold

  const why: WaitReason[] = []
  if (p.paused) {
    why.push({ kind: 'paused', title: 'The pipeline is paused: nothing new starts until it is resumed.', next: 'resume' })
  }
  if (step?.message && isFrozenMessage(step.message) && !TERMINAL_STEP.has(stepState)) {
    why.push({ kind: 'frozen', title: 'Held by a freeze: it promotes once the freeze is lifted, or exempted here.', detail: step.message })
  }
  if (hold) {
    // The version it is held on, as everywhere; the Bundle name is the tooltip.
    const held = bundles.find(b => b.name === hold.bundle)
    const on = held ? bundleVersion(held) : running?.bundle === hold.bundle ? running.version : hold.bundle
    why.push({ kind: 'held', tip: hold.bundle, title: `Held on ${on} by a rollback${hold.createdBy ? `, by ${hold.createdBy}` : ''}: no other version promotes here until it is released.`,
      detail: hold.reason, next: 'release' })
  }
  for (const g of blocking) {
    if (g.approval) {
      const a = g.approval
      const missing = Math.max(0, a.required - a.approved)
      why.push({ kind: 'approval', gate: g, evaluatedAt: g.lastEvaluatedAt, next: 'approve', detail: g.reason,
        title: a.rejected ? `Rejected in ${gateLabel(g)}: a counted reject holds it.`
          : `Needs ${missing} more approval${missing === 1 ? '' : 's'} (${a.approved} of ${a.required}) in ${gateLabel(g)}.` })
    } else if (g.verify) {
      const v = g.verify
      const missing = Math.max(0, v.required - v.passed)
      why.push({ kind: 'verify', gate: g, evaluatedAt: g.lastEvaluatedAt, next: 'override', detail: g.reason,
        title: verifyState(v) === 'failed' ? `Verification ${v.check} failed in ${gateLabel(g)}: ${v.failed} failing verdict${v.failed === 1 ? '' : 's'}.`
          : `Needs ${missing} more passing verdict${missing === 1 ? '' : 's'} (${v.passed} of ${v.required}) for ${v.check} in ${gateLabel(g)}.` })
    } else {
      why.push({ kind: 'gate', gate: g, evaluatedAt: g.lastEvaluatedAt, next: 'override', detail: g.reason,
        title: `Held by gate ${gateLabel(g)}.` })
    }
  }
  if (stepState === 'WaitingForMerge' && step?.prURL) {
    why.push({ kind: 'pr', title: 'Waiting for its pull request to be merged.', prURL: step.prURL, next: 'openPR' })
  }
  // The newest version failed before any step (every environment), or in this
  // environment before its step recorded why (status.failedVersion).
  const f = p.failedVersion
  if (f && (!f.environment || (f.environment === env && stepState !== 'Failed' && stepState !== 'AbortedByAlarm'))) {
    why.push({ kind: 'failed', title: failedVersionTitle(f), detail: f.message })
  }
  if (stepState === 'Failed' || stepState === 'AbortedByAlarm') {
    why.push({ kind: 'failed', title: `${displayState('step', stepState).label}.`,
      detail: failureMessage(step) ?? 'No failure message was recorded: Evidence and History below have the steps and events.',
      next: running ? 'rollback' : undefined })
  }
  if (why.length === 0 && incoming && !IN_FLIGHT.has(stepState) && stepState !== 'Verified') {
    const upstream = (spec?.upstreams ?? []).filter(u => p.environmentStates?.[u] !== 'Verified')
    if (upstream.length > 0) {
      why.push({ kind: 'upstream', title: `Waiting for ${upstream.join(', ')} to be live first.` })
    }
  }

  let state = displayState('step', stepState)
  if (hold) state = heldState(stepState)
  else if (blocking.length > 0) {
    // A CEL gate blocks; an approval gate collecting its quorum, or a verify
    // gate its verdicts, waits on its verifiers.
    const cel = blocking.find(g => !awaitingQuorum(g) && !awaitingVerdicts(g))
    const what = awaitingQuorum(blocking[0]) ? 'approval' : 'verdicts'
    state = cel ? heldByGate(stepState, gateLabel(cel))
      : { ...displayState('gate', 'Waiting'), value: stepState, label: `Waiting for ${what} in ${gateLabel(blocking[0])}` }
  }

  const actions = new Set<StageActionKind>()
  if (blocking.some(g => g.approval)) actions.add('approve').add('reject')
  if (blocking.some(g => !g.approval)) actions.add('override')
  // Promote: the version on its way in has not reached the environment, failed
  // or was stopped by an alarm there, and it is live in every environment the
  // environment waits for (pipelineActions' rule for the shown Bundle; the
  // first environment is started by creating a Bundle). Without a version on
  // its way in, the environment's own state decides.
  const upstreams = spec?.upstreams ?? []
  const stateOf = (bundle: string, e: string) => steps.find(s => s.bundle === bundle && s.environment === e)?.state
    ?? bundles.find(b => b.name === bundle)?.environments?.find(x => x.name === e)?.phase
  // Where the Bundle says nothing about an environment, the Pipeline's state there does.
  const live = (u: string) => (incoming && stateOf(incoming.bundle, u)) ? stateOf(incoming.bundle, u) === 'Verified'
    : p.environmentStates?.[u] === 'Verified'
  const promotable = incoming
    ? PROMOTABLE.has(stateOf(incoming.bundle, env) ?? 'NotStarted') && upstreams.every(live)
    : PROMOTABLE.has(stepState) && upstreams.every(live)
  if (promotable && upstreams.length > 0) actions.add('promote')
  const choices = rollbackChoices(bundles, env, running?.bundle)
  // A choice of version when the Bundles say where they were Verified; else
  // the controller picks the one before (no toBundle).
  if (running && (choices.length > 0 || stepState === 'Verified' || stepState === 'Failed' || stepState === 'AbortedByAlarm')) actions.add('rollback')
  if (hold) actions.add('release')
  actions.add(p.paused ? 'resume' : 'pause')
  if (incoming) actions.add('rejectVersion')
  if (step?.prURL && stepState === 'WaitingForMerge') actions.add('openPR')
  if (spec?.fleet && !fleetOf) {
    // A fleet environment (#1835): pause or resume it, or roll every target back
    // (one rollback for the fleet) once something runs in one. Its targets have their own actions.
    actions.clear()
    actions.add(p.paused ? 'resume' : 'pause').add(spec.fleet.paused ? 'resumeFleet' : 'pauseFleet')
    if (spec.fleet.targets.some(t => p.deployed?.[t])) actions.add('rollback')
  } else if (fleetOf?.fleet) {
    // A fleet target (#1835): its fleet's pause and resume, and the rollback
    // of the whole fleet, next to this target's own actions.
    actions.add(fleetOf.fleet.paused ? 'resumeFleet' : 'pauseFleet')
    if (fleetOf.fleet.targets.some(t => p.deployed?.[t])) actions.add('rollbackFleet')
  }

  // One primary action per state: the approval waiting, the hold or pause to
  // lift, the promotion to start, the PR to merge; a roll back only when the
  // environment failed. Everything else is secondary.
  const failed = stepState === 'Failed' || stepState === 'AbortedByAlarm'
  const primary = PRIMARY.find(k => actions.has(k)) ?? (failed && actions.has('rollback') ? 'rollback' : undefined)

  return { env, spec, fleetOf, running, incoming, state, step, why, gates: envGates, actions, primary, rollbackChoices: choices }
}
