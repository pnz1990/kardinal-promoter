// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect } from 'vitest'
import { pipelineState } from './pipelineState'
import { computeFleetHealth, filterPipelines } from './components/FleetHealthBar'
import type { Pipeline } from './types'

const p = (over: Partial<Pipeline>): Pipeline => ({ name: 'demo', namespace: 'default', phase: 'Ready', environmentCount: 3, activeBundleName: 'demo-1', ...over })

describe('pipelineState', () => {
  it.each<[string, Partial<Pipeline>, string, string]>([
    ['paused', { paused: true, blockerCount: 1 }, 'paused', 'Paused'],
    ['a failed step', { failedStepCount: 1 }, 'failed', 'A version failed'],
    ['an environment that failed', { environments: [{ name: 'prod', state: 'Failed' }] }, 'failed', 'A version failed'],
    ['held, with no environment detail', { phase: 'Promoting', blockerCount: 1 }, 'blocked', 'Blocked by a gate'],
    ['held by two gates, with no environment detail', { phase: 'Promoting', blockerCount: 2 }, 'blocked', 'Blocked by 2 gates'],
    ['held, named from status.environments', { phase: 'Promoting', blockerCount: 1, environments: [
      { name: 'test', state: 'Verified' }, { name: 'uat', state: 'Verified' }, { name: 'prod', state: 'Pending', blockedBy: ['prod-approval'] }] },
      'blocked', 'Held by gate prod-approval in prod'],
    ['held by two gates in two places', { environments: [
      { name: 'prod-eu', state: 'Pending', blockedBy: ['release-approval'] }, { name: 'prod-us', state: 'Pending', blockedBy: ['uat-soak'] }] },
      'blocked', 'Held by gates release-approval and uat-soak in prod-eu and prod-us'],
    ['a step in flight', { environmentStates: { test: 'HealthChecking' } }, 'promoting', 'Promoting'],
    ['a Promoting phase', { phase: 'Promoting' }, 'promoting', 'Promoting'],
    ['no version yet', { phase: 'Unknown', activeBundleName: undefined }, 'empty', 'No version yet'],
    ['up to date', { phase: 'Ready' }, 'healthy', 'Up to date'],
  ])('%s', (_, over, category, label) => {
    const st = pipelineState(p(over))
    expect(st.category).toBe(category)
    expect(st.label).toBe(label)
  })

  // The walkthrough's screen: header "Promoting", badge "Blocked by a gate", counters "0 Promoting
  // / 0 Healthy". Now the counters, the filters and the chips all read the one state.
  it('puts every pipeline in exactly one counter, the one its chip names', () => {
    const fleet = [
      p({ name: 'held', phase: 'Promoting', blockerCount: 1, environmentStates: { test: 'Verified', prod: 'Pending' } }),
      p({ name: 'moving', phase: 'Promoting', environmentStates: { test: 'Promoting' } }),
      p({ name: 'ok' }),
      p({ name: 'broken', failedStepCount: 1, environmentStates: { prod: 'Promoting' } }),
      p({ name: 'stopped', paused: true, blockerCount: 1 }),
    ]
    expect(computeFleetHealth(fleet)).toEqual({ total: 5, healthy: 1, blocked: 1, ciRed: 1, promoting: 1, paused: 1, agents: 0 })
    expect(filterPipelines(fleet, 'blocked').map(x => x.name)).toEqual(['held'])
    expect(filterPipelines(fleet, 'promoting').map(x => x.name)).toEqual(['moving'])
  })

  it('a held pipeline has a compact form for the sidebar: Blocked · N (gates)', () => {
    expect(pipelineState(p({ phase: 'Promoting', blockerCount: 1 })).short).toBe('Blocked · 1')
    expect(pipelineState(p({ phase: 'Promoting', blockerCount: 3 })).short).toBe('Blocked · 3')
    expect(pipelineState(p({ phase: 'Promoting', blockerCount: 2, environments: [{ name: 'prod', blockedBy: ['a', 'b'] }] } as never)).short).toBe('Blocked · 2')
    expect(pipelineState(p({ phase: 'Promoting' })).short).toBeUndefined()
  })
})
