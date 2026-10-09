// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, expect, it } from 'vitest'
import { ageOf, depthGroups, fleetRow } from './fleetModel'
import type { Pipeline } from './types'

const base = (over: Partial<Pipeline>): Pipeline => ({ name: 'app', namespace: 'ns', phase: 'Ready', environmentCount: 3, ...over })
const linear = [{ name: 'test' }, { name: 'uat', upstreams: ['test'] }, { name: 'prod', upstreams: ['uat'] }]

describe('depthGroups', () => {
  const cases: { name: string; p: Pipeline; want: string[][] }[] = [
    { name: 'resolved upstreams, linear', p: base({ environmentTopology: linear }), want: [['test'], ['uat'], ['prod']] },
    {
      name: 'fan-out stacks parallel environments',
      p: base({ environmentTopology: [{ name: 'test' }, { name: 'eu', upstreams: ['test'] }, { name: 'us', upstreams: ['test'] }, { name: 'global', upstreams: ['eu', 'us'] }] }),
      want: [['test'], ['eu', 'us'], ['global']],
    },
    {
      name: 'longest path decides the depth',
      p: base({ environmentTopology: [{ name: 'a' }, { name: 'b', upstreams: ['a'] }, { name: 'c', upstreams: ['a', 'b'] }] }),
      want: [['a'], ['b'], ['c']],
    },
    {
      name: 'no upstreams sent: dependsOn, else the previous entry',
      p: base({ environmentTopology: [{ name: 'test' }, { name: 'uat' }, { name: 'eu', dependsOn: ['uat'] }, { name: 'us', dependsOn: ['uat'] }] }),
      want: [['test'], ['uat'], ['eu', 'us']],
    },
    { name: 'a cycle does not loop', p: base({ environmentTopology: [{ name: 'a', upstreams: ['b'] }, { name: 'b', upstreams: ['a'] }] }), want: [['b'], ['a']] },
    { name: 'no topology: environment states in one group', p: base({ environmentStates: { x: 'Verified', y: 'Pending' } }), want: [['x', 'y']] },
    { name: 'nothing at all', p: base({}), want: [] },
  ]
  for (const c of cases) it(c.name, () => expect(depthGroups(c.p)).toEqual(c.want))
})

describe('fleetRow', () => {
  const deployed = {
    test: { bundle: 'app-2', version: '2.0.0', verifiedAt: '2026-10-01T09:00:00Z' },
    uat: { bundle: 'app-1', version: '1.0.0', verifiedAt: '2026-09-30T09:00:00Z' },
    prod: { bundle: 'app-1', version: '1.0.0', verifiedAt: '2026-09-30T10:00:00Z' },
  }
  const states = (r: ReturnType<typeof fleetRow>) => r.groups.map(g => g.map(s => s.state).join(','))
  const cases: { name: string; p: Pipeline; states: string[]; liveGroup: number; live?: string; incoming?: string }[] = [
    {
      name: 'promoting into uat',
      p: base({ environmentTopology: linear, deployed, activeBundleName: 'app-2', activeBundleVersion: '2.0.0', environmentStates: { test: 'Verified', uat: 'Promoting', prod: 'Pending' } }),
      states: ['settled', 'arriving', 'ahead'], liveGroup: 1, live: 'arriving', incoming: '2.0.0',
    },
    {
      name: 'failed in uat wins over anything moving',
      p: base({ environmentTopology: linear, deployed, activeBundleName: 'app-2', activeBundleVersion: '2.0.0', environmentStates: { test: 'Verified', uat: 'Failed', prod: 'Pending' } }),
      states: ['settled', 'failed', 'ahead'], liveGroup: 1, live: 'failed', incoming: '2.0.0',
    },
    {
      name: 'held by gates before the first environment not reached',
      p: base({ environmentTopology: linear, deployed, blockerCount: 1, activeBundleName: 'app-2', activeBundleVersion: '2.0.0', environmentStates: { test: 'Verified', uat: 'Pending', prod: 'Pending' } }),
      states: ['settled', 'held', 'ahead'], liveGroup: 1, live: 'held', incoming: '2.0.0',
    },
    {
      name: 'all Verified: nothing moving',
      p: base({ environmentTopology: linear, deployed, activeBundleName: 'app-1', activeBundleVersion: '1.0.0', environmentStates: { test: 'Verified', uat: 'Verified', prod: 'Verified' } }),
      states: ['settled', 'settled', 'settled'], liveGroup: -1,
    },
    {
      name: 'never deployed, nothing active',
      p: base({ environmentTopology: linear }),
      states: ['empty', 'empty', 'empty'], liveGroup: -1,
    },
    {
      name: 'falls back to the bundle name without a version',
      p: base({ environmentTopology: linear, activeBundleName: 'app-9', environmentStates: { test: 'WaitingForMerge' } }),
      states: ['arriving', 'ahead', 'ahead'], liveGroup: 0, live: 'arriving', incoming: 'app-9',
    },
    {
      name: 'held before an environment the Bundle has no state for yet',
      p: base({ environmentTopology: linear, deployed, blockerCount: 1, activeBundleName: 'app-2', activeBundleVersion: '2.0.0', environmentStates: { test: 'Verified' } }),
      states: ['settled', 'held', 'ahead'], liveGroup: 1, live: 'held', incoming: '2.0.0',
    },
  ]
  for (const c of cases) {
    it(c.name, () => {
      const r = fleetRow(c.p)
      expect(states(r)).toEqual(c.states)
      expect(r.liveGroup).toBe(c.liveGroup)
      expect(r.live).toBe(c.live)
      expect(r.incomingVersion).toBe(c.incoming)
    })
  }
})

describe('fleetRow: image and config Bundles (#1353)', () => {
  it('a station also names what it runs from another Bundle', () => {
    const r = fleetRow(base({
      environmentTopology: [{ name: 'test' }, { name: 'prod', upstreams: ['test'] }],
      deployed: {
        test: { bundle: 'app-img', version: '1.4.0', configFrom: 'app-cfg', configVersion: 'config abcdef0' },
        prod: { bundle: 'app-cfg', version: 'config abcdef0', imagesFrom: 'app-img', imagesVersion: '1.4.0' },
      },
    }))
    const [[test], [prod]] = r.groups
    expect(test).toMatchObject({ version: '1.4.0', alsoRuns: 'config abcdef0', alsoFrom: 'app-cfg' })
    expect(prod).toMatchObject({ version: 'config abcdef0', alsoRuns: '1.4.0', alsoFrom: 'app-img' })
  })
  it('a station of one Bundle names nothing else', () => {
    const r = fleetRow(base({ environmentTopology: [{ name: 'test' }], deployed: { test: { bundle: 'a', version: '1' } } }))
    expect(r.groups[0][0].alsoRuns).toBeUndefined()
  })
})

describe('ageOf', () => {
  const now = Date.parse('2026-10-01T12:00:00Z')
  const cases: [string | undefined, string][] = [
    [undefined, ''], ['bad', ''], ['2026-10-01T11:59:30Z', 'now'], ['2026-10-01T11:15:00Z', '45m'],
    ['2026-10-01T02:00:00Z', '10h'], ['2026-09-28T12:00:00Z', '3d'],
  ]
  for (const [iso, want] of cases) it(`${iso} → ${want}`, () => expect(ageOf(iso, now)).toBe(want))
})

describe('fleetRow: fleet environments (D1)', () => {
  const targets = ['prod-c01', 'prod-c02', 'prod-c03', 'prod-c04', 'prod-c05']
  const topo = [
    { name: 'test' },
    { name: 'prod', upstreams: ['test'], fleet: { targets, maxConcurrent: 2, maxUnavailable: 1 } },
    { name: 'audit', upstreams: ['prod'] },
  ]
  const base = { name: 'web', namespace: 'team', phase: 'Ready', environmentCount: 3, environmentTopology: topo }

  it('rolls a fleet up into one station between its neighbours', () => {
    const row = fleetRow({
      ...base, activeBundleName: 'web-2', activeBundleVersion: '2.0.0',
      environmentStates: { test: 'Verified', 'prod-c01': 'Verified', 'prod-c02': 'WaitingForMerge', 'prod-c03': 'Promoting' },
      deployed: {
        'prod-c01': { bundle: 'web-2', version: '2.0.0' },
        'prod-c02': { bundle: 'web-1', version: '1.0.0' }, 'prod-c03': { bundle: 'web-1', version: '1.0.0' },
      },
    })
    expect(row.groups.map(g => g.map(s => s.env))).toEqual([['test'], ['prod'], ['audit']])
    const prod = row.groups[1][0]
    expect(prod.state).toBe('arriving')
    expect(prod.version).toBe('1.0.0')
    expect(prod.fleet).toMatchObject({ total: 5, verified: 1, inFlight: 2, failed: 0, pending: 2, maxConcurrent: 2, stopped: false, versions: 2 })
    expect(row.liveGroup).toBe(1)
    expect(row.groups[2][0].state).toBe('ahead')
  })

  it('a failure past maxUnavailable stops the fleet', () => {
    const row = fleetRow({
      ...base, activeBundleName: 'web-2',
      environmentStates: { test: 'Verified', 'prod-c01': 'Verified', 'prod-c02': 'Failed', 'prod-c03': 'Verified' },
    })
    const prod = row.groups[1][0]
    expect(prod.state).toBe('failed')
    expect(prod.fleet).toMatchObject({ verified: 2, failed: 1, inFlight: 0, pending: 2, stopped: true })
    expect(row.live).toBe('failed')
  })

  it('a fleet every target of which runs the active Bundle is settled', () => {
    const states: Record<string, string> = { test: 'Verified' }
    const deployed: Record<string, { bundle: string; version: string }> = {}
    for (const t of targets) { states[t] = 'Verified'; deployed[t] = { bundle: 'web-2', version: '2.0.0' } }
    const prod = fleetRow({ ...base, activeBundleName: 'web-2', environmentStates: states, deployed }).groups[1][0]
    expect(prod.state).toBe('settled')
    expect(prod.version).toBe('2.0.0')
    expect(prod.fleet).toMatchObject({ verified: 5, versions: 1 })
  })
})
