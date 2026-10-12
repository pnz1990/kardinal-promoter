// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// pipelineState.ts — the one state of a Pipeline the UI shows: the page header, the sidebar
// badge, the fleet counters and filters, and the fleet board all read it, so one screen never
// says "Promoting", "Blocked by a gate" and "0 Promoting" about the same pipeline. It comes
// from what the controller wrote (status.environments, the counts and the phase), in the P2
// display labels (displayStates.gen.ts).
import { displayState, heldByGate, type DisplayState } from './displayStates.gen'
import { listOf } from './sentence'
import type { Pipeline } from './types'

/** The bucket a pipeline is counted and filtered in: exactly one per pipeline. */
export type PipelineCategory = 'paused' | 'failed' | 'blocked' | 'promoting' | 'healthy' | 'empty'

export interface PipelineState extends DisplayState {
  category: PipelineCategory
}

/** Step states in which a promotion is running (RollingBack ends; its rollback Bundle's step runs). */
const IN_FLIGHT = new Set(['Promoting', 'WaitingForMerge', 'HealthChecking', 'Verifying'])
const FAILED = new Set(['Failed', 'AbortedByAlarm'])

/** The per-environment step states the UI API serves (status.environments, else environmentStates). */
function states(p: Pipeline): string[] {
  if (p.environments?.length) return p.environments.map(e => e.state ?? '')
  return Object.values(p.environmentStates ?? {})
}

/**
 * The Pipeline's state, in order: paused, a failed version, held by gates (named, with where),
 * promoting, no version yet, up to date.
 */
export function pipelineState(p: Pipeline): PipelineState {
  if (p.paused) return { ...displayState('pipeline', 'Paused'), category: 'paused' }
  const st = states(p)
  if ((p.failedStepCount ?? 0) > 0 || p.phase === 'Degraded' || st.some(s => FAILED.has(s))) {
    return { ...displayState('pipeline', 'Degraded'), category: 'failed' }
  }
  const held = (p.environments ?? []).filter(e => (e.blockedBy?.length ?? 0) > 0)
  if (held.length > 0 || (p.blockerCount ?? 0) > 0) {
    const gates = [...new Set(held.flatMap(e => e.blockedBy ?? []))]
    const where = held.map(e => e.name)
    const base = heldByGate(p.phase || 'Pending', gates.join(', '))
    const label = held.length === 0 ? 'Blocked by a gate'
      : `${gates.length === 1 ? `Held by gate ${gates[0]}` : `Held by gates ${listOf(gates)}`} in ${listOf(where)}`
    return { ...base, label, category: 'blocked' }
  }
  if (st.some(s => IN_FLIGHT.has(s)) || p.phase === 'Promoting') return { ...displayState('pipeline', 'Promoting'), category: 'promoting' }
  if (!p.activeBundleName && (p.phase === 'Unknown' || !p.phase)) return { ...displayState('pipeline', 'Unknown'), category: 'empty' }
  return { ...displayState('pipeline', 'Ready'), category: 'healthy' }
}
