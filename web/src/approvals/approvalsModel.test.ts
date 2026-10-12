// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect } from 'vitest'
import { approvalItems, approvalsWaiting } from './approvalsModel'
import type { Pipeline, PolicyGate } from '../types'

const g = (name: string, over: Partial<PolicyGate> = {}, approval: Partial<NonNullable<PolicyGate['approval']>> = {}): PolicyGate => ({
  name, namespace: 'team', expression: 'true', ready: false, state: 'Block', holding: true, pipeline: 'web', bundle: 'web-3',
  environment: 'prod', gateTemplate: 'release-approval', createdAt: '2026-10-11T09:00:00Z',
  approval: { required: 2, approved: 1, decisions: [{ user: 'bob', decision: 'approve', counted: true }], ...approval }, ...over,
})
const pipelines: Pipeline[] = [{ name: 'web', namespace: 'team', phase: 'Promoting', environmentCount: 3, activeBundleName: 'web-3', activeBundleVersion: '1.5.0' }]
const alice = { username: 'alice', groups: ['release-managers'] }

describe('approvalItems (U7)', () => {
  it('lists approval gates still collecting their quorum, oldest wait first, by version and template name', () => {
    const gates = [
      g('new', { environment: 'uat', createdAt: '2026-10-11T09:30:00Z' }),
      g('old'),
      g('passing', { holding: false, state: 'Pass' }),
      g('met', {}, { approved: 2 }),
      g('declined', {}, { rejected: true }),
      g('cel', { approval: undefined }),
      g('template', { template: true, bundle: undefined }),
    ]
    const { mine, others } = approvalItems(gates, pipelines, alice)
    expect(mine.map(i => i.gate.name)).toEqual(['old', 'new'])
    expect(others).toEqual([])
    expect(mine[0]).toMatchObject({ pipeline: 'web', env: 'prod', version: '1.5.0', gateName: 'release-approval', approved: 1, required: 2 })
  })

  it('puts those waiting on someone else apart, with why', () => {
    const gates = [
      g('approved-by-me', {}, { decisions: [{ user: 'alice', decision: 'approve', counted: true }] }),
      g('not-counted', {}, { decisions: [{ user: 'alice', decision: 'approve', counted: false, reason: 'the Bundle\'s creator (excludeAuthor)' }] }),
      g('other-group', {}, { allowedGroups: ['dba'] }),
      g('mine'),
    ]
    const { mine, others } = approvalItems(gates, pipelines, alice)
    expect(mine.map(i => i.gate.name)).toEqual(['mine'])
    expect(others.map(i => i.notMine)).toEqual([
      'You approved it; it needs someone else.',
      'Your approval does not count: the Bundle\'s creator (excludeAuthor).',
      'Only members of dba can approve it.',
    ])
    expect(approvalsWaiting(gates, alice)).toBe(1)
  })

  it('without an identity every waiting approval is listed (the page says why it cannot record one)', () => {
    expect(approvalItems([g('a')], pipelines, null).mine).toHaveLength(1)
  })
})
