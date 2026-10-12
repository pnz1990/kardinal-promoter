// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect } from 'vitest'
import { actionCheck, mayDecide, restriction } from './actionAccess'
import type { PolicyGate } from '../types'

const gate = (allowedUsers?: string[], allowedGroups?: string[]): PolicyGate => ({
  name: 'release-approval--web-3-prod', namespace: 'team', expression: 'true', ready: false, state: 'Block', holding: true,
  approval: { required: 2, approved: 1, allowedUsers, allowedGroups },
})
const alice = { username: 'alice', groups: ['sre'] }

describe('actionAccess (U7)', () => {
  it('names the permission each action needs', () => {
    expect(actionCheck('approve', {})).toEqual({ verb: 'create', resource: 'approvals' })
    expect(actionCheck('override', { gate: 'g' })).toEqual({ verb: 'update', resource: 'policygates', subresource: 'override', name: 'g' })
    expect(actionCheck('ship', {})).toEqual({ verb: 'create', resource: 'bundles' })
    expect(actionCheck('resume', { pipeline: 'web' })).toEqual({ verb: 'update', resource: 'pipelines', subresource: 'pause', name: 'web' })
    expect(actionCheck('release', { pipeline: 'web' })).toEqual({ verb: 'update', resource: 'pipelines', subresource: 'hold', name: 'web' })
    expect(actionCheck('rejectVersion', { bundle: 'web-3' })).toEqual({ verb: 'update', resource: 'bundles', subresource: 'reject', name: 'web-3' })
    expect(actionCheck('openPR', {})).toBeUndefined()
  })

  it('says why an action is not available, in words the person can act on', () => {
    const no = { allowed: false, known: true, grantedBy: 'promoter or admin' }
    expect(restriction('pause', no, alice)).toBe('Pausing the pipeline needs the promoter or admin role.')
    expect(restriction('pause', { allowed: true, known: true }, alice)).toBeUndefined()
    expect(restriction('pause', { allowed: false, known: false }, alice)).toBeUndefined()
    expect(restriction('approve', { allowed: true, known: true }, null)).toMatch(/^Approving from the UI names who decided, so it needs sign-in/)
    expect(restriction('approve', { allowed: true, known: true }, alice, gate(['bob'], ['release-managers']), 'release-approval'))
      .toBe('Only members of release-managers or bob can decide on release-approval.')
    expect(restriction('reject', { allowed: true, known: true }, alice, gate(undefined, ['sre']))).toBeUndefined()
  })

  it('anyone with the role decides on a gate that lists nobody', () => {
    expect(mayDecide(gate(), alice)).toBe(true)
    expect(mayDecide(gate(['alice']), alice)).toBe(true)
    expect(mayDecide(gate(['bob'], ['dev']), alice)).toBe(false)
  })
})
