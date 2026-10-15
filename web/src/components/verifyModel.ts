// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// components/verifyModel.ts — a verify gate's state and words
// (docs/agentic/verify-with-agents.md), without React: VerifyQuorum draws
// them, and the stage drawer's wait reasons say them (#1908).

import type { GateVerify } from '../types'
import type { Tone } from '../ui/tone'

/** The gate's state from its counts (the reason line is the gate's). */
export function verifyState(v: GateVerify): 'verified' | 'failed' | 'waiting' {
  if (v.failed > (v.maxFailures ?? 0)) return 'failed'
  if (v.passed >= v.required) return 'verified'
  return 'waiting'
}

/** What the verdicts amount to, in words. */
export function verifySummary(v: GateVerify): string {
  switch (verifyState(v)) {
    case 'failed': return `Failed (${v.failed} failing ${v.failed === 1 ? 'verdict' : 'verdicts'})`
    case 'verified': return `Verified (${v.passed} of ${v.required})`
  }
  const n = inconclusiveWaiting(v)
  const inc = n ? `; ${n} inconclusive, ${v.onInconclusive === 'retry' ? 'retrying' : 'waiting'}` : ''
  return `${v.passed} of ${v.required} ${v.required === 1 ? 'verdict' : 'verdicts'} for ${v.check}${inc}`
}

/** Who may verify, in words. */
export function verifierRule(v: GateVerify): string {
  const agents = v.allowedAgents?.length ? `agents ${v.allowedAgents.join(', ')}` : 'no agents'
  const runs = v.template ? `; runs ${v.template} in the cluster` : ''
  return `${v.check} in ${v.environment}, from ${agents} and the people allowed${runs}; inconclusive: ${v.onInconclusive}`
}

/** A verdict's result as a chip: its tone and words, never hue alone. */
export function verdictChip(result: string, onInconclusive: string): { tone: Tone; label: string; glyph: string } {
  switch (result) {
    case 'pass': return { tone: 'verified', label: 'Pass', glyph: '✓' }
    case 'fail': return { tone: 'failed', label: 'Fail', glyph: '✕' }
    case 'inconclusive':
      return { tone: onInconclusive === 'fail' ? 'failed' : 'waiting', label: `Inconclusive (${INCONCLUSIVE[onInconclusive] ?? onInconclusive})`, glyph: '?' }
  }
  return { tone: 'unknown', label: result, glyph: '·' }
}

/** What an inconclusive verdict does, by spec.verify.onInconclusive. */
const INCONCLUSIVE: Record<string, string> = { hold: 'holds', retry: 'retries', fail: 'counts as a fail' }

/** How many inconclusive verdicts wait (counted ones are fails, under onInconclusive: fail). */
export function inconclusiveWaiting(v: GateVerify): number {
  return (v.verdicts ?? []).filter(d => d.result === 'inconclusive' && !d.counted).length
}
