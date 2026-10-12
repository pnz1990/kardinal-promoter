// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, expect, it } from 'vitest'
import type { Bundle, GraphResponse, Pipeline } from '../types'
import { buildModel, edgeGateLabel, gatesOn, gridOrder, groupSummary, stationLabel, type GateMark, type StationMapModel } from './model'
import { octilinear, layout, levelAt, initialLevel, trimEnd, lengthOf, gridCells, gridRows, pointBeforeEnd, threadPoints, gateOffset, wrapWords, THREAD_GAP_PX, gridSpace, gridSize, gridText, groupTitle, CH_EM, TEXT_ASCENT, type ZoomLevel } from './geometry'

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

  it('a holding gate stops the thread and counts as blocked whatever the step does (a PR may be open)', () => {
    const graph: GraphResponse = {
      nodes: [step('test', 'Verified'), step('uat', 'Verified'), gate('g1', 'no-weekend-deploys', 'Block', true), step('prod', 'WaitingForMerge')],
      edges: [{ from: 'step-test', to: 'step-uat' }, { from: 'step-uat', to: 'g1' }, { from: 'g1', to: 'step-prod' }],
    }
    const m = buildModel(threeEnvs(), { bundle: bundle('app-v2', '1.4.2'), graph })
    expect(m.thread.find(t => t.to === 'prod')?.kind).toBe('stopped')
    expect(m.attention.blocked).toEqual([{ env: 'prod', gate: 'no-weekend-deploys' }])
    expect(m.stations.get('prod')!.display.label).toBe('Held by gate no-weekend-deploys')
  })

  it('a thread into a PR waiting for its merge holds still; into a held environment it is ink', () => {
    const waiting = buildModel(threeEnvs(), { bundle: bundle('app-v2', '1.4.2'), graph: {
      nodes: [step('test', 'Verified'), step('uat', 'Verified'), step('prod', 'WaitingForMerge')], edges: [] } })
    expect(waiting.thread.find(t => t.to === 'prod')?.kind).toBe('wait')
    const p = threeEnvs({ environmentTopology: [{ name: 'test' }, { name: 'uat', upstreams: ['test'] },
      { name: 'prod', upstreams: ['uat'], hold: { bundle: 'app-rollback-x', reason: 'bad release' } }] })
    const held = buildModel(p, { bundle: bundle('app-v2', '1.4.2'), graph: { nodes: [step('test', 'Verified'), step('uat', 'Verified')], edges: [] } })
    expect(held.thread.find(t => t.to === 'prod')?.kind).toBe('held')
  })

  it('following an older Bundle does not show it as incoming where a newer one runs', () => {
    const older = { ...bundle('app-v0', '1.4.0'), createdAt: '2026-10-01T00:00:00Z' }
    const runs = { ...bundle('app-v1', '1.4.1'), createdAt: '2026-10-05T00:00:00Z' }
    const m = buildModel(threeEnvs(), { bundle: older, graph: { nodes: [step('test', 'Verified'), step('uat', 'Verified'), step('prod', 'Failed')], edges: [] } },
      { bundles: [older, runs] })
    expect(m.stations.get('prod')!.incoming).toBeUndefined()
    expect(m.stations.get('test')!.incoming, 'test runs app-v2; app-v0 verified there long ago').toBeUndefined()
  })

  it('joins an approval gate to its PolicyGate: quorum on the mark, and the environment waits for a person', () => {
    const graph: GraphResponse = {
      nodes: [step('test', 'Verified'), step('uat', 'Verified'), { ...gate('gate-release-approval-app-v2-prod', 'release-approval', 'Waiting'), environment: 'prod' }, step('prod', 'NotStarted')],
      edges: [{ from: 'gate-release-approval-app-v2-prod', to: 'step-prod' }],
    }
    const m = buildModel(threeEnvs(), { bundle: bundle('app-v2', '1.4.2'), graph }, {
      gates: [{ name: 'release-approval-app-v2-prod', namespace: 'default', expression: 'true', ready: false, state: 'Waiting', environment: 'prod', bundle: 'app-v2',
        approval: { required: 2, approved: 1 } }],
    })
    expect(m.stations.get('prod')!.gates[0].approval).toMatchObject({ required: 2, approved: 1 })
    expect(m.attention.waiting).toEqual(['prod'])
    expect(stationLabel(m.stations.get('prod')!, () => '')).toContain('release-approval has 1 of 2 approvals')
  })

  it('a fleet with a minority of failures keeps its thread; it snaps only when the fleet halted', () => {
    const targets = ['p1', 'p2', 'p3', 'p4', 'p5']
    const p: Pipeline = { name: 'f', namespace: 'default', phase: 'Ready', environmentCount: 6, activeBundleName: 'b2', topologyResolved: true,
      environmentTopology: [{ name: 'test' }, { name: 'prod', upstreams: ['test'], fleet: { targets, maxConcurrent: 2, maxUnavailable: 2 } }] }
    const at = (failed: number) => buildModel(p, { bundle: bundle('b2', '2'), graph: { nodes: [step('test', 'Verified'),
      ...targets.map((t, i) => step(t, i < failed ? 'Failed' : i < 3 ? 'Verified' : 'Promoting'))], edges: [] } })
    const one = at(1)
    expect(one.thread.find(t => t.to === 'platform:prod')?.kind).toBe('flow')
    expect(one.groups.get('platform:prod')).toMatchObject({ failed: 1, halted: false })
    expect(groupSummary(one.groups.get('platform:prod')!)).toBe('1 failed, 2 promoting, 2 live')
    const two = at(2)
    expect(two.thread.find(t => t.to === 'platform:prod')?.kind).toBe('snap')
    expect(two.groups.get('platform:prod')!.halted).toBe(true)
  })

  it('an expanded grid starts a row per state, and every cell has its row and column', () => {
    const targets = Array.from({ length: 30 }, (_, i) => `t${String(i).padStart(2, '0')}`)
    const p: Pipeline = { name: 'f', namespace: 'default', phase: 'Ready', environmentCount: 30, activeBundleName: 'b2',
      environmentTopology: [{ name: 'prod', fleet: { targets } }] }
    const m = buildModel(p, { bundle: bundle('b2', '2'), graph: { nodes: targets.map((t, i) => step(t, i < 3 ? 'Failed' : 'Verified')), edges: [] } },
      { expanded: new Set(['platform:prod']) })
    const g = m.groups.get('platform:prod')!
    const rows = gridRows(g)
    expect(rows[0]).toMatchObject({ start: 0, count: 3, label: { text: '3 failed' } })
    expect(rows[1].label?.text).toBe('27 live')
    const cells = gridCells(g, gridOrder(g, m.stations), { x: 0, y: 0, w: 0, h: 0 })
    expect(cells.filter(c => c.row === 0).map(c => c.env)).toEqual(['t00', 't01', 't02'])
    expect(cells[3]).toMatchObject({ row: 1, col: 0 })
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

  it('keeps a straight run into a node long enough for its gates (never a gate on the chamfer)', () => {
    for (const b of [[160, 40], [160, -90], [90, 30]] as Array<[number, number]>) {
      const pts = octilinear([0, 0], b, 12, 46)
      const p = pointBeforeEnd(pts, 30)
      expect(Math.abs(p.dy), JSON.stringify(pts)).toBeLessThan(0.01)
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

/** Lays out m and gives each thread segment into a gated node with the gate's
 *  index, the distance its drawn thread ends before the edge's end, and the
 *  vertical slope at every gate mark. */
function threadIntoGates(m: StationMapModel, level: ZoomLevel, k = 1) {
  const byId = new Map(m.nodes.map(n => [n.id, n]))
  const lay = layout(m.nodes, m.edges, level, id => (byId.get(id) ? gatesOn(byId.get(id)!, m).length : 0))
  return m.thread.map(seg => {
    const e = lay.edges.find(x => x.edge.from === seg.from && x.edge.to === seg.to)!
    const gates = gatesOn(byId.get(seg.to)!, m)
    const holdAt = gates.findIndex(g => g.holding)
    const drawn = threadPoints(e.points, seg.kind, holdAt, k)
    return { seg, holdAt, short: lengthOf(e.points) - lengthOf(drawn) }
  })
}

describe('the thread and the gates on the map', () => {
  const holdGraph = (env: string, extra: GraphResponse['nodes'] = []): GraphResponse => ({
    nodes: [step('test', 'Verified'), { ...gate(`g-${env}`, 'freeze', 'Block', true), environment: env }, ...extra],
    edges: [{ from: 'step-test', to: `g-${env}` }, { from: `g-${env}`, to: `step-${env}` }],
  })
  const cases: Array<[string, Pipeline, GraphResponse, string]> = [
    ['a plain station', threeEnvs({ environmentTopology: [{ name: 'test' }, { name: 'uat', upstreams: ['test'] }] }),
      holdGraph('uat', [step('uat', 'WaitingForMerge')]), 'uat'],
    ['a held station', threeEnvs({ environmentTopology: [{ name: 'test' }, { name: 'uat', upstreams: ['test'], hold: { bundle: 'app-rb', reason: 'x' } }] }),
      holdGraph('uat'), 'uat'],
    ['a band', { name: 'w', namespace: 'default', phase: 'Ready', environmentCount: 7, topologyResolved: true, activeBundleName: 'app-v2',
      environmentTopology: [{ name: 'test' }, ...['w1', 'w2', 'w3', 'w4', 'w5', 'w6'].map(n => ({ name: n, upstreams: ['test'] }))] },
      { ...holdGraph('w1'), nodes: [...holdGraph('w1').nodes, ...['w2', 'w3', 'w4', 'w5', 'w6'].map(n => step(n, 'Verified'))] }, 'band:1'],
    ['a platform', { name: 'f', namespace: 'default', phase: 'Ready', environmentCount: 4, topologyResolved: true, activeBundleName: 'app-v2',
      environmentTopology: [{ name: 'test' }, { name: 'prod', upstreams: ['test'], fleet: { targets: ['p1', 'p2', 'p3'] } }] },
      { ...holdGraph('p1'), nodes: [...holdGraph('p1').nodes, step('p2', 'Verified'), step('p3', 'Verified')] }, 'platform:prod'],
  ]
  for (const [what, p, graph, target] of cases) {
    for (const k of [1, 0.25]) {
      it(`into ${what} that a gate holds, the thread ends a clear gap before the bar (zoom ${k})`, () => {
        const m = buildModel(p, { bundle: bundle('app-v2', '1.4.2'), graph })
        const into = threadIntoGates(m, 'map', k).find(t => t.seg.to === target)
        expect(into, JSON.stringify(m.thread)).toBeDefined()
        expect(into!.holdAt).toBeGreaterThanOrEqual(0)
        expect(into!.short).toBeGreaterThanOrEqual(gateOffset(into!.holdAt) + THREAD_GAP_PX / k - 0.01)
      })
    }
  }

  for (const level of ['overview', 'map', 'detail'] as const) {
    it(`gate marks sit on a straight run at ${level}`, () => {
      const p: Pipeline = { name: 'c', namespace: 'default', phase: 'Ready', environmentCount: 6, topologyResolved: true, activeBundleName: 'app-v2',
        environmentTopology: [{ name: 'test' }, { name: 'a', upstreams: ['test'] }, { name: 'b', upstreams: ['test'] }, { name: 'c', upstreams: ['test'] },
          { name: 'audit', upstreams: ['a', 'b', 'c'] }, { name: 'dr', upstreams: ['a'] }] }
      const graph: GraphResponse = {
        nodes: [step('test', 'Verified'), step('a', 'Verified'), step('b', 'Verified'), step('c', 'Verified'),
          { ...gate('g1', 'freeze', 'Block', true), environment: 'audit' }, { ...gate('g2', 'soak', 'Pass'), environment: 'audit' }],
        edges: [{ from: 'step-a', to: 'g1' }, { from: 'g1', to: 'g2' }, { from: 'g2', to: 'step-audit' }],
      }
      const m = buildModel(p, { bundle: bundle('app-v2', '1.4.2'), graph })
      const byId = new Map(m.nodes.map(n => [n.id, n]))
      const lay = layout(m.nodes, m.edges, level, id => (byId.get(id) ? gatesOn(byId.get(id)!, m).length : 0))
      const into = lay.edges.filter(e => e.edge.to === 'audit')
      expect(into.length).toBe(3)
      for (const e of into) {
        for (let i = 0; i < 2; i++) expect(Math.abs(pointBeforeEnd(e.points, gateOffset(i)).dy), `${e.edge.from} gate ${i}`).toBeLessThan(0.01)
      }
    })
  }
})

describe('wrapWords', () => {
  it('wraps at spaces and hyphens, and ends in an ellipsis at a word boundary', () => {
    expect(wrapWords('Held by gate no-weekend-deploys-in-eu-and-us-regions', 24, 2))
      .toEqual(['Held by gate no-weekend-', 'deploys-in-eu-and-us-…'])
    expect(wrapWords('Waiting for PR merge', 40, 2)).toEqual(['Waiting for PR merge'])
    for (const l of wrapWords('Held by gate no-weekend-deploys-in-eu-and-us-regions', 16, 2)) expect(l.length).toBeLessThanOrEqual(16)
  })
})

describe('an expanded grid at every level', () => {
  // checkout-30's fleet (12 targets in four states) and large-300's prod fleet (149 in four).
  const fleet = (n: number, states: string[]) => {
    const targets = Array.from({ length: n }, (_, i) => `t${String(i).padStart(3, '0')}`)
    const p: Pipeline = { name: 'f', namespace: 'default', phase: 'Ready', environmentCount: n, activeBundleName: 'b2',
      environmentTopology: [{ name: 'edge', fleet: { targets, maxConcurrent: 2, maxUnavailable: 2 } }] }
    const graph = { nodes: targets.map((t, i) => step(t, states[i % states.length])), edges: [] }
    return buildModel(p, { bundle: bundle('b2', '2'), graph }, { expanded: new Set(['platform:edge']) }).groups.get('platform:edge')!
  }
  const overlap = (a: { x: number; y: number; w: number; h: number }, b: typeof a) =>
    a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h
  for (const [what, g] of [['12 targets', fleet(12, ['Verified', 'Failed', 'Promoting', 'NotStarted', 'Verified', 'Verified'])],
    ['149 targets', fleet(149, ['Verified', 'Verified', 'Verified', 'Failed', 'Promoting', 'NotStarted'])]] as const) {
    for (const level of ['overview', 'map', 'detail'] as const) {
      it(`${what} at ${level}: its title, row labels, footer and cells never overlap`, () => {
        const size = gridSize(g, level)
        const box = { x: 0, y: 0, w: size.w, h: size.h }
        const { u } = gridSpace(level)
        const t = gridText(g, box, level)
        const textBox = (x: number, y: number, chars: number) => ({ x, y: y - u * TEXT_ASCENT, w: chars * CH_EM * u, h: u })
        const texts = [
          { name: 'title', r: textBox(12, t.title.y, groupTitle(g).length) },
          ...t.rows.map(r => ({ name: r.text, r: textBox(r.x, r.y, r.text.length) })),
          { name: 'footer', r: textBox(t.foot.x, t.foot.y, 30) },
        ]
        const cells = gridCells(g, gridOrder(g, new Map(g.members.map(m => [m, { display: { tone: 'verified', label: '' } }])) as never), box, level)
          .map(c => ({ x: c.x, y: c.y, w: 10, h: 10 }))
        for (let i = 0; i < texts.length; i++) {
          for (let j = i + 1; j < texts.length; j++) expect(overlap(texts[i].r, texts[j].r), `${texts[i].name} × ${texts[j].name}`).toBe(false)
          for (const c of cells) expect(overlap(texts[i].r, c), `${texts[i].name} × a cell`).toBe(false)
          expect(texts[i].r.y + texts[i].r.h, `${texts[i].name} inside the frame`).toBeLessThanOrEqual(box.h + 0.01)
        }
      })
    }
  }
})

describe('edgeGateLabel', () => {
  const gate = (name: string, over: Partial<GateMark> = {}): GateMark =>
    ({ id: `gate-${name}`, name, state: 'Pass', holding: false, display: { value: 'Pass', label: 'Passing', glyph: '✓', ascii: '+', tone: 'verified' }, ...over })
  const blocking = { state: 'Block', holding: true, display: { value: 'Block', label: 'Blocking', glyph: '▮', ascii: '#', tone: 'blocked' as const } }
  it.each<[string, GateMark[], string]>([
    ['one gate', [gate('freeze')], 'freeze'],
    ['a holding approval, then a passing gate', [gate('prod-approval', { ...blocking, approval: { required: 1, approved: 0 } }), gate('business-hours')],
      'prod-approval (waiting) · +1 passing'],
    ['a blocking gate, then others', [gate('uat-soak', blocking), gate('a', { state: 'Waiting' }), gate('b')], 'uat-soak (blocking) · +2 more'],
  ])('%s', (_, gates, want) => {
    expect(edgeGateLabel(gates)).toBe(want)
  })
})
