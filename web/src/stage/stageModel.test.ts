// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, expect, it } from 'vitest'
import { stageView, rollbackChoices, type StageInput } from './stageModel'
import type { Bundle, Pipeline, PolicyGate, PromotionStep } from '../types'

const pipeline = (over: Partial<Pipeline> = {}): Pipeline => ({
  name: 'web', namespace: 'team', phase: 'Promoting', environmentCount: 3, activeBundleName: 'web-3',
  environmentTopology: [{ name: 'test' }, { name: 'uat', upstreams: ['test'] }, { name: 'prod', upstreams: ['uat'], approval: 'pr-review' }],
  environmentStates: { test: 'Verified', uat: 'Verified', prod: 'Pending' },
  deployed: { prod: { bundle: 'web-2', version: '1.4.0', verifiedAt: '2026-10-10T10:00:00Z' } },
  ...over,
})
const bundle = (name: string, tag: string, created: string, envs: Bundle['environments'] = []): Bundle =>
  ({ name, namespace: 'team', phase: 'Verified', type: 'image', pipeline: 'web', createdAt: created,
    images: [{ repository: 'ghcr.io/org/web', tag }], environments: envs })
const bundles = [
  bundle('web-1', '1.3.0', '2026-10-01T00:00:00Z', [{ name: 'prod', phase: 'Verified', healthCheckedAt: '2026-10-02T00:00:00Z' }]),
  bundle('web-2', '1.4.0', '2026-10-05T00:00:00Z', [{ name: 'prod', phase: 'Verified' }]),
  { ...bundle('web-3', '1.5.0', '2026-10-09T00:00:00Z'), phase: 'Promoting' },
]
const gate = (over: Partial<PolicyGate>): PolicyGate => ({ name: 'no-weekend', namespace: 'team', expression: '!schedule.isWeekend',
  ready: false, pipeline: 'web', bundle: 'web-3', environment: 'prod', state: 'Block', holding: true,
  reason: 'weekend deploys are frozen', lastEvaluatedAt: '2026-10-11T09:00:00Z', ...over })
const input = (over: Partial<StageInput> = {}): StageInput => ({ env: 'prod', pipeline: pipeline(), bundles, steps: [], gates: [], ...over })

describe('stageView', () => {
  it('a gate holding prod: why first names the gate with its message and age, and offers an override', () => {
    const v = stageView(input({ gates: [gate({}), gate({ name: 'other-env', environment: 'uat' })] }))
    expect(v.running).toMatchObject({ bundle: 'web-2', version: '1.4.0' })
    expect(v.incoming).toEqual({ bundle: 'web-3', version: '1.5.0' })
    expect(v.state.label).toBe('Held by gate no-weekend')
    expect(v.why).toHaveLength(1)
    expect(v.why[0]).toMatchObject({ kind: 'gate', title: 'Held by gate no-weekend.', detail: 'weekend deploys are frozen',
      evaluatedAt: '2026-10-11T09:00:00Z', next: 'override' })
    expect([...v.actions]).toEqual(expect.arrayContaining(['override', 'rollback', 'pause', 'rejectVersion']))
    expect(v.actions.has('approve')).toBe(false)
  })

  it('an approval gate: the quorum still missing, and approve and reject', () => {
    const approval = gate({ name: 'two-approvers', approval: { required: 2, approved: 1, decisions: [] } })
    const v = stageView(input({ gates: [approval] }))
    expect(v.why[0]).toMatchObject({ kind: 'approval', title: 'Needs 1 more approval (1 of 2) in two-approvers.', next: 'approve' })
    expect(v.actions.has('approve')).toBe(true)
    expect(v.actions.has('reject')).toBe(true)
  })

  it('paused and held come before the gates; release and resume are offered', () => {
    const p = pipeline({ paused: true, environmentTopology: [{ name: 'test' }, { name: 'uat' },
      { name: 'prod', hold: { bundle: 'web-1', reason: 'bad 1.4.0', createdBy: 'alice' } }] })
    const v = stageView(input({ pipeline: p, gates: [gate({})] }))
    expect(v.why.map(w => w.kind)).toEqual(['paused', 'held', 'gate'])
    expect(v.state.label).toBe('Held by rollback')
    expect(v.actions.has('release')).toBe(true)
    expect(v.actions.has('resume')).toBe(true)
    expect(v.actions.has('pause')).toBe(false)
  })

  it('waiting for the PR: a link to it', () => {
    const step: PromotionStep = { name: 's', namespace: 'team', pipeline: 'web', bundle: 'web-3', environment: 'prod',
      stepType: 'pr-review', state: 'WaitingForMerge', prURL: 'https://github.com/o/r/pull/7' }
    const v = stageView(input({ steps: [step] }))
    expect(v.state.label).toBe('Waiting for PR merge')
    expect(v.why[0]).toMatchObject({ kind: 'pr', prURL: 'https://github.com/o/r/pull/7', next: 'openPR' })
  })

  it('an upstream not live yet is the reason when nothing else holds it', () => {
    const p = pipeline({ environmentStates: { test: 'Verified', uat: 'HealthChecking', prod: 'NotStarted' } })
    expect(stageView(input({ pipeline: p })).why).toEqual([{ kind: 'upstream', title: 'Waiting for uat to be live first.' }])
  })

  it('a failed promotion offers the rollback', () => {
    const step: PromotionStep = { name: 's', namespace: 'team', pipeline: 'web', bundle: 'web-3', environment: 'prod',
      stepType: 'auto', state: 'Failed', message: 'health check timeout' }
    const v = stageView(input({ steps: [step] }))
    expect(v.state.label).toBe('Failed')
    expect(v.why[0]).toMatchObject({ kind: 'failed', title: 'Failed.', detail: 'health check timeout', next: 'rollback' })
  })

  it('a fleet target reads its fleet entry', () => {
    const p = pipeline({ environmentTopology: [{ name: 'prod', fleet: { targets: ['prod-eu', 'prod-us'] } }],
      deployed: {}, environmentStates: {} })
    const v = stageView(input({ env: 'prod-eu', pipeline: p }))
    expect(v.fleetOf?.name).toBe('prod')
  })
})

describe('rollbackChoices', () => {
  it('lists the versions Verified in the environment, newest first, not the one running nor a rejected one', () => {
    const rejected = { ...bundle('web-0', '1.2.0', '2026-09-01T00:00:00Z', [{ name: 'prod', phase: 'Verified' }]),
      rejected: { reason: 'CVE', by: 'bob' } }
    expect(rollbackChoices([...bundles, rejected], 'prod', 'web-2')).toEqual([
      { bundle: 'web-1', version: '1.3.0', verifiedAt: '2026-10-02T00:00:00Z' }])
  })
})

describe('stageView: promote', () => {
  it('offers Promote where the environment is not reached and every upstream is live', () => {
    const p = pipeline({ environmentStates: { test: 'Verified', uat: 'Verified', prod: 'NotStarted' }, deployed: {} })
    expect(stageView(input({ pipeline: p })).actions.has('promote')).toBe(true)
    const notYet = pipeline({ environmentStates: { test: 'Verified', uat: 'Promoting', prod: 'NotStarted' }, deployed: {} })
    expect(stageView(input({ pipeline: notYet })).actions.has('promote')).toBe(false)
    expect(stageView(input({ env: 'test', pipeline: p })).actions.has('promote')).toBe(false)
  })
})
