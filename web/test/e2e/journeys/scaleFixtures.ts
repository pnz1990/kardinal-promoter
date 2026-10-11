// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// scaleFixtures.ts — deterministic pipelines of 30 and 300 environments for
// the station map journeys (017), served over the mock server with page.route
// like journey 014's wave, so the other journeys' fixtures are untouched.
//
//   checkout-30: test → uat-a, uat-b → stage → canary → a wave of 6 prod
//     regions → a fleet "edge" of 12 targets (2 at a time) → dr, audit, ...;
//     the followed Bundle is verified to the prod wave, promoting into edge,
//     one edge target failed, audit blocked by a gate.
//   large-300: dev → stg → wave 1 (12) → wave 2 (48) → wave 3 (8) → fleet
//     "prod" (149 targets, 8 at a time, stop at 5 failed) → fleet "edge" (80)
//     → audit (held). 3 prod targets failed, 8 promoting, 26 not reached.

import type { Page } from '@playwright/test'

type Env = { name: string; upstreams?: string[]; fleet?: { targets: string[]; maxConcurrent?: number; maxUnavailable?: number }; hold?: Record<string, unknown> }

const NOW = Date.now()
const ago = (min: number) => new Date(NOW - min * 60_000).toISOString()
const pad = (n: number, w = 2) => String(n).padStart(w, '0')

interface Fixture {
  pipeline: Record<string, unknown>
  bundles: Array<Record<string, unknown>>
  graph: { nodes: Array<Record<string, unknown>>; edges: Array<{ from: string; to: string }> }
  /** The previous Bundle's graph: Verified everywhere (compare). */
  previousGraph: { nodes: Array<Record<string, unknown>>; edges: Array<{ from: string; to: string }> }
  gates: Array<Record<string, unknown>>
}

function build(name: string, ns: string, topo: Env[], states: Record<string, string>, opts: {
  version: string; previous: string; gates?: Array<{ env: string; gate: string; state: string; holding?: boolean; expression: string; reason?: string }>
  /** Older versions before the previous one (the Matrix's rows), Verified everywhere. */
  older?: number
}): Fixture {
  const bundle = `${name}-7`
  const prev = `${name}-6`
  // Every environment, fleets expanded.
  const envs = topo.flatMap(e => e.fleet ? e.fleet.targets : [e.name])
  const targetsOf = new Map(topo.filter(e => e.fleet).map(e => [e.name, e.fleet!.targets]))
  const expand = (ups: string[] = []) => ups.flatMap(u => targetsOf.get(u) ?? [u])
  const upstreamsOf = new Map<string, string[]>()
  for (const e of topo) {
    for (const t of e.fleet ? e.fleet.targets : [e.name]) upstreamsOf.set(t, expand(e.upstreams))
  }
  const deployed = Object.fromEntries(envs.map(env => {
    const runsNew = states[env] === 'Verified' || states[env] === 'HealthChecking' || states[env] === 'Verifying'
    return [env, runsNew
      ? { bundle, version: opts.version, verifiedAt: states[env] === 'Verified' ? ago(30) : undefined }
      : { bundle: prev, version: opts.previous, verifiedAt: ago(60 * 26) }]
  }))
  const nodes: Fixture['graph']['nodes'] = envs.map(env => ({
    id: `step-${env}`, type: 'PromotionStep', label: env, environment: env, state: states[env] ?? 'NotStarted',
    ...(states[env] && states[env] !== 'NotStarted' ? { startedAt: ago(45) } : {}),
    ...(states[env] === 'Failed' ? { message: 'health check timed out after 5m' } : {}),
  }))
  const edges: Fixture['graph']['edges'] = []
  const gates: Fixture['gates'] = []
  const gateInto = new Map<string, string[]>()
  for (const g of opts.gates ?? []) {
    const id = `gate-${g.gate}-${g.env}`
    nodes.push({ id, type: 'PolicyGate', label: g.gate, environment: g.env, state: g.state, holding: !!g.holding,
      expression: g.expression, message: g.reason, lastEvaluatedAt: ago(0.2) })
    edges.push({ from: id, to: `step-${g.env}` })
    gateInto.set(g.env, [...(gateInto.get(g.env) ?? []), id])
    gates.push({ name: `${g.gate}-${bundle}-${g.env}`, namespace: ns, pipeline: name, bundle, environment: g.env,
      expression: g.expression, ready: g.state === 'Pass', holding: !!g.holding, state: g.state, reason: g.reason, lastEvaluatedAt: ago(0.2) })
  }
  for (const env of envs) {
    const entries = gateInto.get(env) ?? [`step-${env}`]
    for (const u of upstreamsOf.get(env) ?? []) for (const to of entries) edges.push({ from: `step-${u}`, to })
  }
  const phase = Object.values(states).some(s => s === 'Failed') ? 'Degraded' : 'Ready'
  return {
    pipeline: {
      name, namespace: ns, phase, environmentCount: envs.length, activeBundleName: bundle, activeBundleVersion: opts.version,
      blockerCount: (opts.gates ?? []).filter(g => g.holding).length,
      failedStepCount: Object.values(states).filter(s => s === 'Failed').length,
      environmentTopology: topo.map(e => ({ name: e.name, ...(e.upstreams ? { upstreams: e.upstreams } : {}), ...(e.fleet ? { fleet: e.fleet } : {}), ...(e.hold ? { hold: e.hold } : {}) })),
      topologyResolved: true,
      environmentStates: Object.fromEntries(envs.filter(e => states[e]).map(e => [e, states[e]])),
      deployed,
    },
    bundles: [
      { name: bundle, namespace: ns, phase: 'Promoting', type: 'image', pipeline: name, createdAt: ago(50),
        images: [{ repository: `ghcr.io/acme/${name}`, tag: opts.version }], provenance: { author: 'dana', commitSHA: 'a1b2c3d4e5f6' },
        environments: envs.filter(e => states[e]).map(e => ({ name: e, phase: states[e] })) },
      { name: prev, namespace: ns, phase: 'Superseded', type: 'image', pipeline: name, createdAt: ago(60 * 27),
        images: [{ repository: `ghcr.io/acme/${name}`, tag: opts.previous }], environments: envs.map(e => ({ name: e, phase: 'Verified' })) },
      ...Array.from({ length: opts.older ?? 0 }, (_, i) => ({
        name: `${name}-old-${pad(i + 1, 3)}`, namespace: ns, phase: 'Superseded', type: 'image', pipeline: name, createdAt: ago(60 * (28 + i)),
        images: [{ repository: `ghcr.io/acme/${name}`, tag: `0.${i}` }], environments: envs.map(e => ({ name: e, phase: 'Verified' })),
      })),
    ],
    graph: { nodes, edges },
    previousGraph: {
      nodes: envs.map(env => ({ id: `step-${env}`, type: 'PromotionStep', label: env, environment: env, state: 'Verified' })),
      edges: edges.filter(e => e.from.startsWith('step-') && e.to.startsWith('step-')),
    },
    gates,
  }
}

/** checkout-30: 30 environments. */
export function checkout30(): Fixture {
  const regions = ['eu-1', 'eu-2', 'eu-3', 'us-1', 'us-2', 'us-3'].map(r => `prod-${r}`)
  const edge = Array.from({ length: 12 }, (_, i) => `edge-${pad(i + 1)}`)
  const topo: Env[] = [
    { name: 'test' },
    { name: 'uat-a', upstreams: ['test'] }, { name: 'uat-b', upstreams: ['test'] },
    { name: 'stage', upstreams: ['uat-a', 'uat-b'] },
    { name: 'canary', upstreams: ['stage'] },
    ...regions.map(r => ({ name: r, upstreams: ['canary'] })),
    { name: 'edge', upstreams: regions, fleet: { targets: edge, maxConcurrent: 2, maxUnavailable: 2 } },
    { name: 'dr', upstreams: ['edge'] }, { name: 'audit', upstreams: ['edge'] },
    { name: 'perf', upstreams: ['stage'] }, { name: 'sec', upstreams: ['stage'] },
    { name: 'smoke-1', upstreams: ['audit'] }, { name: 'smoke-2', upstreams: ['audit'] }, { name: 'smoke-3', upstreams: ['audit'] },
  ]
  const states: Record<string, string> = {}
  for (const e of ['test', 'uat-a', 'uat-b', 'stage', 'canary', 'perf', 'sec', ...regions]) states[e] = 'Verified'
  edge.slice(0, 5).forEach(e => { states[e] = 'Verified' })
  states['edge-06'] = 'Failed'
  states['edge-07'] = 'Promoting'
  return build('checkout-30', 'scale', topo, states, {
    version: '1.4.2', previous: '1.4.1',
    gates: [{ env: 'audit', gate: 'change-freeze', state: 'Block', holding: true, expression: '!changewindow["q4-freeze"]', reason: 'Q4 change freeze is active' }],
  })
}

/** large-300: 300 environments. */
export function large300(): Fixture {
  const w1 = Array.from({ length: 12 }, (_, i) => `w1-${pad(i + 1)}`)
  const w2 = Array.from({ length: 48 }, (_, i) => `w2-${pad(i + 1)}`)
  const w3 = Array.from({ length: 8 }, (_, i) => `w3-${pad(i + 1)}`)
  const regions = ['eu-west', 'eu-cent', 'us-east', 'ap-south']
  const prod = Array.from({ length: 149 }, (_, i) => `prod-${regions[i % 4]}-${pad(Math.floor(i / 4) + 1)}`)
  const edge = Array.from({ length: 80 }, (_, i) => `edge-${pad(i + 1)}`)
  const topo: Env[] = [
    { name: 'dev' }, { name: 'stg', upstreams: ['dev'] },
    ...w1.map(n => ({ name: n, upstreams: ['stg'] })),
    ...w2.map(n => ({ name: n, upstreams: w1 })),
    ...w3.map(n => ({ name: n, upstreams: w2 })),
    { name: 'prod', upstreams: w3, fleet: { targets: prod, maxConcurrent: 8, maxUnavailable: 5 } },
    { name: 'edge', upstreams: ['prod'], fleet: { targets: edge, maxConcurrent: 10 } },
    { name: 'audit', upstreams: ['edge'], hold: { bundle: 'large-300-rollback-1', reason: 'audit trail rebuild', createdBy: 'ops' } },
  ]
  const states: Record<string, string> = {}
  for (const e of ['dev', 'stg', ...w1, ...w2, ...w3]) states[e] = 'Verified'
  prod.forEach((e, i) => {
    if (i < 112) states[e] = 'Verified'
    else if (i < 115) states[e] = 'Failed'
    else if (i < 123) states[e] = 'Promoting'
  })
  // 200 versions in all, for the Matrix.
  return build('large-300', 'scale', topo, states, { version: '2.1.0', previous: '2.0.9', older: 198 })
}

/** serveScale routes the scale fixtures in front of the mock server. */
export async function serveScale(page: Page) {
  const fixtures = [checkout30(), large300()]
  await page.route('**/api/v1/ui/pipelines', async route => {
    const res = await route.fetch()
    await route.fulfill({ response: res, json: [...await res.json(), ...fixtures.map(f => f.pipeline)] })
  })
  await page.route('**/api/v1/ui/gates**', async route => {
    const res = await route.fetch()
    await route.fulfill({ response: res, json: [...await res.json(), ...fixtures.flatMap(f => f.gates)] })
  })
  for (const f of fixtures) {
    const name = f.pipeline.name as string
    const bundle = f.bundles[0].name as string
    await page.route(`**/api/v1/ui/pipelines/${name}/bundles**`, route => route.fulfill({ json: f.bundles }))
    await page.route(`**/api/v1/ui/bundles/${bundle}/graph**`, route => route.fulfill({ json: f.graph }))
    await page.route(`**/api/v1/ui/bundles/${f.bundles[1].name as string}/graph**`, route => route.fulfill({ json: f.previousGraph }))
    await page.route(`**/api/v1/ui/bundles/${bundle}/steps**`, route => route.fulfill({ json: [] }))
  }
}
