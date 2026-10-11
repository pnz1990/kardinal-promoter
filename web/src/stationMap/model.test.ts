// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, expect, it } from 'vitest'
import type { Bundle, GraphResponse, Pipeline } from '../types'
import { buildModel, gridOrder, stationLabel } from './model'
import { octilinear, layout, levelAt, initialLevel, trimEnd, lengthOf } from './geometry'

const bundle = (name: string, tag: string): Bundle => ({
  name, namespace: 'default', phase: 'Promoting', type: 'image', pipeline: 'app', images: [{ repository: 'ghcr.io/acme/app', tag }],
})

const step = (env: string, state: string, extra: Partial<GraphResponse['nodes'][number]> = {}) =>
  ({ id: `step-${env}`, type: 'PromotionStep' as const, label: env, environment: env, state, ...extra })

const gate = (id: string, name: string, state: string, holding = false) =>
  ({ id, type: 'PolicyGate' as const, label: name, environment: name, state, holding, expression: '!schedule.isWeekend' })

function threeEnvs(over: Partial<Pipeline> = {}): Pipeline {
  return {
    name: 'app', namespace: 'default', phase: 'Ready', environmentCount: 3,
    activeBundleName: 'app-v2',
    environmentTopology: [{ name: 'test' }, { name: 'uat', upstreams: ['test'] }, { name: 'prod', upstreams: ['uat'] }],
    deployed: {
      test: { bundle: 'app-v2', version: '1.4.2', verifiedAt: '2026-10-11T09:00:00Z' },
      uat: { bundle: 'app-v2', version: '1.4.2', verifiedAt: '2026-10-11T09:40:00Z' },
      prod: { bundle: 'app-v1', version: '1.4.1', verifiedAt: '2026-10-09T09:00:00Z' },
    },
    ...over,
  }
}

describe('buildModel', () => {
  it('shows what each environment runs, not the followed Bundle (the PipelineLaneView.tsx:207 bug)', () => {
    const graph: GraphResponse = {
      nodes: [step('test', 'Verified'), step('uat', 'Verified'), gate('g1', 'no-weekend-deploys', 'Block', true), step('prod', 'Pending')],
      edges: [{ from: 'step-test', to: 'step-uat' }, { from: 'step-uat', to: 'g1' }, { from: 'g1', to: 'step-prod' }],
    }
    const m = buildModel(threeEnvs(), { bundle: bundle('app-v2', '1.4.2'), graph })
    const prod = m.stations.get('prod')!
    expect(prod.running?.version).toBe('1.4.1')
    expect(prod.incoming?.version).toBe('1.4.2')
    expect(prod.display.tone).toBe('blocked')
    expect(prod.display.label).toBe('Held by gate no-weekend-deploys')
    expect(prod.gates.map(g => g.name)).toEqual(['no-weekend-deploys'])
    expect(m.stations.get('uat')!.running?.version).toBe('1.4.2')
    expect(m.stations.get('uat')!.incoming, 'uat runs the followed Bundle: nothing incoming').toBeUndefined()
    expect(m.attention.blocked).toEqual([{ env: 'prod', gate: 'no-weekend-deploys' }])
  })

  it('reads what runs and what is incoming from Pipeline status.environments (P1) when the API sends it', () => {
    const p = threeEnvs({
      deployed: undefined,
      environments: [
        { name: 'test', bundle: 'app-v2', version: '1.4.2', state: 'Verified', since: '2026-10-11T09:00:00Z' },
        { name: 'uat', bundle: 'app-v2', version: '1.4.2', state: 'Verified' },
        { name: 'prod', bundle: 'app-v1', version: '1.4.1', incomingBundle: 'app-v2', incomingVersion: '1.4.2 (images)', state: 'WaitingForMerge', prURL: 'https://github.com/acme/app/pull/42' },
      ],
    })
    const m = buildModel(p, { bundle: bundle('app-v2', '1.4.2'), graph: {
      nodes: [step('test', 'Verified'), step('uat', 'Verified'), step('prod', 'WaitingForMerge')], edges: [] } })
    const prod = m.stations.get('prod')!
    expect(prod.running).toMatchObject({ bundle: 'app-v1', version: '1.4.1' })
    expect(prod.incoming).toMatchObject({ version: '1.4.2 (images)', prURL: 'https://github.com/acme/app/pull/42' })
    expect(prod.display.label).toBe('Waiting for PR merge')
    expect(m.stations.get('test')!.running?.verifiedAt).toBe('2026-10-11T09:00:00Z')
  })

  it('draws the thread solid through verified, flowing into promoting, stopped before a blocking gate, snapped at a failure', () => {
    const p = threeEnvs({ environmentTopology: [{ name: 'test' }, { name: 'uat', upstreams: ['test'] }, { name: 'prod', upstreams: ['uat'] }, { name: 'dr', upstreams: ['uat'] }] })
    const graph: GraphResponse = {
      nodes: [step('test', 'Verified'), step('uat', 'Verified'), gate('g1', 'no-weekend-deploys', 'Block', true), step('prod', 'Pending'), step('dr', 'Failed')],
      edges: [{ from: 'step-test', to: 'step-uat' }, { from: 'step-uat', to: 'g1' }, { from: 'g1', to: 'step-prod' }, { from: 'step-uat', to: 'step-dr' }],
    }
    const m = buildModel(p, { bundle: bundle('app-v2', '1.4.2'), graph })
    const kinds = Object.fromEntries(m.thread.map(t => [`${t.from}→${t.to}`, t.kind]))
    expect(kinds).toEqual({ 'test→uat': 'solid', 'uat→prod': 'stopped', 'uat→dr': 'snap' })
    expect(m.attention.failed).toEqual(['dr'])

    const promoting = buildModel(threeEnvs(), { bundle: bundle('app-v2', '1.4.2'), graph: {
      nodes: [step('test', 'Verified'), step('uat', 'HealthChecking')], edges: [{ from: 'step-test', to: 'step-uat' }] } })
    expect(promoting.thread).toEqual([{ from: 'test', to: 'uat', kind: 'flow' }])
  })

  it('shows a held environment as held, not as the followed Bundle', () => {
    const p = threeEnvs({ environmentTopology: [{ name: 'test' }, { name: 'uat', upstreams: ['test'] },
      { name: 'prod', upstreams: ['uat'], hold: { bundle: 'app-rollback-x', reason: 'bad release' } }] })
    const m = buildModel(p, { bundle: bundle('app-v2', '1.4.2'), graph: { nodes: [step('test', 'Verified')], edges: [] } })
    expect(m.stations.get('prod')!.display.tone).toBe('held')
    expect(m.attention.held).toEqual(['prod'])
  })

  it('collapses a wave of 5 or more into a band, and a fleet into a platform', () => {
    const wave = Array.from({ length: 6 }, (_, i) => ({ name: `w${i}`, upstreams: ['test'] }))
    const p: Pipeline = {
      name: 'big', namespace: 'default', phase: 'Ready', environmentCount: 9, topologyResolved: true,
      environmentTopology: [{ name: 'test' }, ...wave,
        { name: 'prod', upstreams: wave.map(w => w.name), fleet: { targets: ['prod-eu', 'prod-us', 'prod-ap'], maxConcurrent: 1, maxUnavailable: 1 } },
        { name: 'audit', upstreams: ['prod'] }],
      deployed: { 'prod-eu': { bundle: 'b1', version: '2.0' }, 'prod-us': { bundle: 'b1', version: '2.0' } },
    }
    const m = buildModel(p, undefined)
    expect([...m.groups.keys()].sort()).toEqual(['band:1', 'platform:prod'])
    expect(m.groups.get('platform:prod')!.version).toBe('2.0')
    expect(m.groups.get('platform:prod')!.maxConcurrent).toBe(1)
    expect(m.nodes.map(n => n.id)).toEqual(['test', 'band:1', 'platform:prod', 'audit'])
    expect(m.edges).toEqual([
      { from: 'test', to: 'band:1' }, { from: 'band:1', to: 'platform:prod' }, { from: 'platform:prod', to: 'audit' },
    ])
    // Expanded, a group stays one layout node, drawn as its grid.
    const ex = buildModel(p, undefined, { expanded: new Set(['band:1']) })
    expect(ex.nodes.find(n => n.id === 'band:1')).toMatchObject({ kind: 'group', expanded: true })
    expect(m.stations.get('prod-us')!.upstreams).toEqual(wave.map(w => w.name))
  })

  it('orders a grid most urgent first, then by name', () => {
    const p: Pipeline = {
      name: 'f', namespace: 'default', phase: 'Ready', environmentCount: 3, activeBundleName: 'b2',
      environmentTopology: [{ name: 'prod', fleet: { targets: ['prod-c', 'prod-a', 'prod-b'] } }],
    }
    const m = buildModel(p, { bundle: bundle('b2', '2'), graph: { nodes: [step('prod-a', 'Verified'), step('prod-b', 'Failed'), step('prod-c', 'Promoting')], edges: [] } })
    expect(gridOrder(m.groups.get('platform:prod')!, m.stations)).toEqual(['prod-b', 'prod-c', 'prod-a'])
  })

  it('names a station for a screen reader', () => {
    const m = buildModel(threeEnvs(), { bundle: bundle('app-v2', '1.4.2'), graph: { nodes: [step('test', 'Verified'), step('uat', 'Verified')], edges: [] } })
    expect(stationLabel(m.stations.get('prod')!, () => '2d')).toBe('prod — Not reached, runs 1.4.1, 2d ago')
    expect(stationLabel(m.stations.get('uat')!, () => '1h')).toBe('uat — Live, runs 1.4.2, 1h ago')
  })
})

describe('geometry', () => {
  it('routes edges octilinear: horizontal runs and 45° bends only', () => {
    for (const [a, b] of [[[0, 0], [200, 0]], [[0, 0], [200, 60]], [[0, 60], [200, 0]], [[0, 0], [40, 300]]] as Array<[[number, number], [number, number]]>) {
      const pts = octilinear(a, b, 12)
      expect(pts[0]).toEqual(a)
      expect(pts[pts.length - 1]).toEqual(b)
      for (let i = 1; i < pts.length; i++) {
        const dx = Math.abs(pts[i][0] - pts[i - 1][0])
        const dy = Math.abs(pts[i][1] - pts[i - 1][1])
        const ok = dy < 0.01 || dx < 0.01 || Math.abs(dx - dy) < 0.01
        expect(ok, `segment ${i} of ${JSON.stringify(pts)}`).toBe(true)
      }
    }
  })

  it('trims a polyline before its end (the thread stops before a gate bar)', () => {
    const pts: Array<[number, number]> = [[0, 0], [100, 0], [130, 30]]
    const cut = trimEnd(pts, 10)
    expect(lengthOf(pts) - lengthOf(cut)).toBeCloseTo(10, 5)
  })

  it('opens 3 environments at Detail, 30 at Map, 300 at Overview, and maps zoom to levels', () => {
    expect([initialLevel(3), initialLevel(30), initialLevel(300)]).toEqual(['detail', 'map', 'overview'])
    expect([levelAt(0.3), levelAt(0.8), levelAt(1.5)]).toEqual(['overview', 'map', 'detail'])
  })

  it('lays out stations left to right by depth', () => {
    const m = buildModel(threeEnvs(), undefined)
    const l = layout(m.nodes, m.edges, 'detail')
    const xs = ['test', 'uat', 'prod'].map(id => l.placed.get(id)!.x)
    expect(xs[0]).toBeLessThan(xs[1])
    expect(xs[1]).toBeLessThan(xs[2])
    expect(l.edges).toHaveLength(2)
  })
})
