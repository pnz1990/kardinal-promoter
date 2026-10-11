// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// fixtures.mjs — the demo world the recordings show: a "shop" namespace with a few
// Pipelines that each illustrate one flow, served in place of the UI API
// (/api/v1/ui/*) with the shapes of cmd/kardinal-controller/ui_api.go. Every
// timestamp is relative to NOW, and the page's clock starts at NOW, so a clip
// reads the same ("verified 4m ago") on every run.
//
// The state is per browser context and changes the way the controller would
// answer: an approval counts, a rollback starts promoting.

/** The demo's wall-clock time: a Tuesday afternoon, inside business hours. */
export const NOW = Date.parse('2026-10-06T14:30:00Z')

const NS = 'shop'
const ago = (ms) => new Date(NOW - ms).toISOString()
const MIN = 60_000

/** Pipeline metadata for the list. */
function pipeline(name, extra) {
  return { name, namespace: NS, phase: 'Ready', blockerCount: 0, failedStepCount: 0, topologyResolved: true, ...extra }
}

function env(name, upstreams, extra = {}) {
  return { name, ...(upstreams.length ? { upstreams } : {}), ...extra }
}

function step(id, environment, state, extra = {}) {
  return { id: `step-${id}`, type: 'PromotionStep', label: environment, environment, state, ...extra }
}

function gate(id, environment, state, expression, extra = {}) {
  return { id: `gate-${id}`, type: 'PolicyGate', label: id, environment, state, expression, lastEvaluatedAt: ago(20_000), ...extra }
}

function bundle(pipelineName, version, phase, minutesAgo, extra = {}) {
  return {
    name: `${pipelineName}-${version.replaceAll('.', '-')}`, namespace: NS, phase, type: 'image', pipeline: pipelineName,
    createdAt: ago(minutesAgo * MIN),
    images: [{ repository: `ghcr.io/shop/${pipelineName}`, tag: version }],
    provenance: { commitSHA: sha(pipelineName + version), author: 'ci-bot', ciRunURL: `https://github.com/shop/${pipelineName}/actions/runs/${1000 + minutesAgo}` },
    ...extra,
  }
}

/** A stable, made-up commit SHA. */
function sha(seed) {
  let h = 2166136261
  for (const c of seed) h = Math.imul(h ^ c.charCodeAt(0), 16777619) >>> 0
  return (h.toString(16).padStart(8, '0') + 'c0ffee5e1ec7ab1e5eed0123456789abcdef').slice(0, 40)
}

/**
 * status.summary as pkg/reconciler/pipeline Summarize writes it: one clause per version and
 * state ("<version> <label, lowercased> in <envs>"), the version said once while it repeats,
 * clauses joined by "; ".
 */
function summarize(clauses) {
  let last = ''
  return clauses.map(([version, what, envs]) => {
    const s = `${version === last ? '' : `${version} `}${what} in ${envs.join(', ')}`
    last = version
    return s
  }).join('; ')
}

const deployed = (bundleName, version, minutesAgo) => ({ bundle: bundleName, version, verifiedAt: ago(minutesAgo * MIN) })

/** Edge points-of-presence for the big fleet. */
const POPS = ['ams', 'arn', 'atl', 'bom', 'cdg', 'cpt', 'dfw', 'dub', 'eze', 'fra', 'gru', 'hel', 'hkg', 'iad', 'icn', 'jnb',
  'kix', 'lax', 'lhr', 'lis', 'mad', 'mel', 'mex', 'mia', 'mil', 'nrt', 'ord', 'otp', 'prg', 'scl', 'sea', 'sin',
  'sjc', 'sof', 'syd', 'tlv', 'tpe', 'vie', 'waw', 'yul', 'yyz', 'zrh']

/** Filler Pipelines for the big fleet, so the board scrolls. */
const FILLER = ['accounts', 'billing', 'catalog', 'cart', 'notifications', 'pricing', 'recommendations', 'reviews',
  'shipping', 'tax', 'wishlist', 'loyalty']

/**
 * The fixtures. `big` adds the edge fleet and a dozen more Pipelines (the big-fleet
 * clip); the other clips use the small world, so the board fits on screen.
 */
export function createFixtures({ big = false } = {}) {
  // ── checkout: test → uat → prod-eu, prod-us; 1.8.0 waits on two gates ──────────
  const co180 = bundle('checkout', '1.8.0', 'Promoting', 42, {
    environments: [
      { name: 'test', phase: 'Verified' }, { name: 'uat', phase: 'Verified' },
      { name: 'prod-eu', phase: 'Pending' }, { name: 'prod-us', phase: 'Pending' },
    ],
  })
  const co172 = bundle('checkout', '1.7.2', 'Superseded', 26 * 60, {
    environments: ['test', 'uat', 'prod-eu', 'prod-us'].map((name) => ({ name, phase: 'Verified' })),
  })
  const co171 = bundle('checkout', '1.7.1', 'Superseded', 3 * 24 * 60, {
    environments: ['test', 'uat', 'prod-eu', 'prod-us'].map((name) => ({ name, phase: 'Verified' })),
  })
  const approval = {
    name: `release-approval--${co180.name}-prod-eu`, gateTemplate: 'release-approval', namespace: NS, pipeline: 'checkout', bundle: co180.name, environment: 'prod-eu',
    expression: 'true', ready: false, holding: true, state: 'Block', reason: 'waiting for approvals: 1 of 2 (alice)',
    lastEvaluatedAt: ago(20_000),
    approval: {
      required: 2, allowedGroups: ['release-managers'], excludeAuthor: true, approved: 1,
      decisions: [{ user: 'alice', decision: 'approve', counted: true, comment: 'canary error rate flat', firstSeenAt: ago(25 * MIN) }],
    },
  }
  const soak = {
    name: `uat-soak--${co180.name}-prod-us`, gateTemplate: 'uat-soak', namespace: NS, pipeline: 'checkout', bundle: co180.name, environment: 'prod-us',
    expression: 'upstream.uat.soakMinutes >= 30', ready: false, holding: true, state: 'Block',
    reason: 'uat has soaked 12m of 30m', lastEvaluatedAt: ago(15_000),
  }

  // ── storefront: test → staging → prod, all live; prod can be rolled back ─────────
  const sf230 = bundle('storefront', '2.3.0', 'Verified', 3 * 60, {
    environments: ['test', 'staging', 'prod'].map((name) => ({ name, phase: 'Verified' })),
  })
  const sf229 = bundle('storefront', '2.2.9', 'Superseded', 2 * 24 * 60, {
    environments: ['test', 'staging', 'prod'].map((name) => ({ name, phase: 'Verified' })),
  })

  // ── search: test → prod; 4.1.0 failed its prod health check ───────────────────────
  const se403 = bundle('search', '4.0.3', 'Superseded', 4 * 24 * 60, {
    environments: ['test', 'prod'].map((name) => ({ name, phase: 'Verified', healthCheckedAt: ago(4 * 24 * 60 * MIN) })),
  })
  const se410 = bundle('search', '4.1.0', 'Failed', 75, {
    environments: [{ name: 'test', phase: 'Verified' }, { name: 'prod', phase: 'Failed' }],
  })

  // ── edge-cdn: canary → a fleet of 42 points of presence ───────────────────────────
  const targets = POPS.map((p) => `pops-${p}`)
  const edge = bundle('edge-cdn', '7.2.0', 'Promoting', 35)
  const edgeStates = Object.fromEntries([['canary', 'Verified'], ...targets.map((t, i) => [t, i < 27 ? 'Verified' : i < 32 ? 'Promoting' : 'Pending'])])

  const state = {
    pipelines: [
      pipeline('checkout', {
        phase: 'Promoting', environmentCount: 4, activeBundleName: co180.name, activeBundleVersion: '1.8.0', blockerCount: 2,
        summary: summarize([['1.8.0', 'live', ['test', 'uat']], ['1.8.0', 'held by gate release-approval', ['prod-eu']], ['1.8.0', 'held by gate uat-soak', ['prod-us']]]),
        environmentStates: { test: 'Verified', uat: 'Verified', 'prod-eu': 'Pending', 'prod-us': 'Pending' },
        environmentTopology: [env('test', []), env('uat', ['test']), env('prod-eu', ['uat']), env('prod-us', ['uat'])],
        deployed: {
          test: deployed(co180.name, '1.8.0', 31), uat: deployed(co180.name, '1.8.0', 14),
          'prod-eu': deployed(co172.name, '1.7.2', 25 * 60), 'prod-us': deployed(co172.name, '1.7.2', 25 * 60),
        },
        inventoryAgeDays: 0, lastMergedAt: ago(14 * MIN),
      }),
      pipeline('storefront', {
        environmentCount: 3, activeBundleName: sf230.name, activeBundleVersion: '2.3.0',
        summary: summarize([['2.3.0', 'live', ['test', 'staging', 'prod']]]),
        environmentStates: { test: 'Verified', staging: 'Verified', prod: 'Verified' },
        environmentTopology: [env('test', []), env('staging', ['test']), env('prod', ['staging'])],
        deployed: { test: deployed(sf230.name, '2.3.0', 170), staging: deployed(sf230.name, '2.3.0', 150), prod: deployed(sf230.name, '2.3.0', 120) },
        inventoryAgeDays: 0, lastMergedAt: ago(120 * MIN),
      }),
      pipeline('search', {
        phase: 'Degraded', environmentCount: 2, activeBundleName: se410.name, activeBundleVersion: '4.1.0', failedStepCount: 1,
        summary: summarize([['4.1.0', 'live', ['test']], ['4.1.0', 'failed', ['prod']]]),
        environmentStates: { test: 'Verified', prod: 'Failed' },
        environmentTopology: [env('test', []), env('prod', ['test'])],
        deployed: { test: deployed(se410.name, '4.1.0', 70), prod: deployed('search-4-0-3', '4.0.3', 4 * 24 * 60) },
        inventoryAgeDays: 0, lastMergedAt: ago(70 * MIN),
      }),
    ],
    bundles: {
      checkout: [co180, co172, co171],
      storefront: [sf230, sf229],
      search: [se410, se403],
      'edge-cdn': [edge],
    },
    graphs: {
      [co180.name]: {
        nodes: [
          step('co-test', 'test', 'Verified', { startedAt: ago(40 * MIN) }),
          step('co-uat', 'uat', 'Verified', { startedAt: ago(30 * MIN) }),
          gate('release-approval', 'prod-eu', 'Block', 'true', { holding: true, message: approval.reason }),
          step('co-prod-eu', 'prod-eu', 'Pending'),
          gate('uat-soak', 'prod-us', 'Block', soak.expression, { holding: true, message: soak.reason }),
          step('co-prod-us', 'prod-us', 'Pending'),
        ],
        edges: [
          { from: 'step-co-test', to: 'step-co-uat' },
          { from: 'step-co-uat', to: 'gate-release-approval' }, { from: 'gate-release-approval', to: 'step-co-prod-eu' },
          { from: 'step-co-uat', to: 'gate-uat-soak' }, { from: 'gate-uat-soak', to: 'step-co-prod-us' },
        ],
      },
      ...Object.fromEntries([co172, co171].map((b) => [b.name, {
        nodes: ['test', 'uat', 'prod-eu', 'prod-us'].map((e) => step(`${b.name}-${e}`, e, 'Verified')),
        edges: [
          { from: `step-${b.name}-test`, to: `step-${b.name}-uat` },
          { from: `step-${b.name}-uat`, to: `step-${b.name}-prod-eu` },
          { from: `step-${b.name}-uat`, to: `step-${b.name}-prod-us` },
        ],
      }])),
      ...Object.fromEntries([sf230, sf229].map((b) => [b.name, {
        nodes: ['test', 'staging', 'prod'].map((e) => step(`${b.name}-${e}`, e, b === sf230 ? 'Verified' : 'Superseded')),
        edges: [{ from: `step-${b.name}-test`, to: `step-${b.name}-staging` }, { from: `step-${b.name}-staging`, to: `step-${b.name}-prod` }],
      }])),
      [se410.name]: {
        nodes: [
          step('se-test', 'test', 'Verified'),
          step('se-prod', 'prod', 'Failed', { message: 'health check: deployment search-prod has 0/3 ready replicas after 10m' }),
        ],
        edges: [{ from: 'step-se-test', to: 'step-se-prod' }],
      },
      [edge.name]: {
        nodes: [step('edge-canary', 'canary', 'Verified'), ...targets.map((t) => step(`edge-${t}`, t, edgeStates[t]))],
        edges: targets.map((t) => ({ from: 'step-edge-canary', to: `step-edge-${t}` })),
      },
    },
    steps: {
      [se410.name]: [{
        name: 'search-prod-4-1-0', namespace: NS, pipeline: 'search', bundle: se410.name, environment: 'prod',
        stepType: 'kustomize-set-image', state: 'Failed', currentStepIndex: 5,
        message: 'health check: deployment search-prod has 0/3 ready replicas after 10m (ImagePullBackOff)',
        steps: [['git-clone', 2100, 'Completed'], ['kustomize-set-image', 300, 'Completed'], ['git-commit', 200, 'Completed'], ['open-pr', 1600, 'Completed'],
          ['wait-for-merge', 60_000, 'Completed'], ['health-check', 600_000, 'Failed']].reduce((acc, [name, ms, state]) => {
          const from = acc.length ? Date.parse(acc[acc.length - 1].completedAt) : NOW - 75 * MIN
          acc.push({ name, state, startedAt: new Date(from).toISOString(), completedAt: new Date(from + ms).toISOString(), durationMs: ms })
          return acc
        }, []),
      }],
      [co180.name]: ['test', 'uat'].map((e, i) => ({
        name: `checkout-${e}-1-8-0`, namespace: NS, pipeline: 'checkout', bundle: co180.name, environment: e,
        stepType: 'kustomize-set-image', state: 'Verified', currentStepIndex: 5,
        steps: [['git-clone', 2400], ['kustomize-set-image', 300], ['git-commit', 200], ['open-pr', 1800], ['wait-for-merge', 95_000], ['health-check', 41_000]]
          .reduce((acc, [name, ms]) => {
            const from = acc.length ? Date.parse(acc[acc.length - 1].completedAt) : NOW - (40 - i * 10) * MIN
            acc.push({ name, state: 'Completed', startedAt: new Date(from).toISOString(), completedAt: new Date(from + ms).toISOString(), durationMs: ms })
            return acc
          }, []),
      })),
    },
    gates: [approval, soak],
  }

  if (big) {
    state.pipelines.push(pipeline('edge-cdn', {
      phase: 'Promoting', environmentCount: 2, activeBundleName: edge.name, activeBundleVersion: '7.2.0',
      summary: summarize([['7.2.0', 'live', ['canary', ...targets.slice(0, 27)]], ['7.2.0', 'promoting', targets.slice(27, 32)], ['7.2.0', 'queued', targets.slice(32)]]),
      environmentStates: edgeStates,
      environmentTopology: [env('canary', []), env('pops', ['canary'], { fleet: { targets, maxConcurrent: 5, maxUnavailable: 2 } })],
      deployed: Object.fromEntries(Object.entries(edgeStates).filter(([, s]) => s === 'Verified').map(([e], i) => [e, deployed(edge.name, '7.2.0', 30 - (i % 25))])),
    }))
    for (const [i, name] of FILLER.entries()) {
      const version = `${1 + (i % 4)}.${(i * 7) % 10}.${i % 3}`
      const b = bundle(name, version, 'Verified', 60 + i * 47)
      state.bundles[name] = [b]
      state.pipelines.push(pipeline(name, {
        environmentCount: 3, activeBundleName: b.name, activeBundleVersion: version,
        summary: summarize([[version, 'live', ['test', 'staging', 'prod']]]),
        environmentStates: { test: 'Verified', staging: 'Verified', prod: i % 5 === 2 ? 'WaitingForMerge' : 'Verified' },
        environmentTopology: [env('test', []), env('staging', ['test']), env('prod', ['staging'])],
        deployed: { test: deployed(b.name, version, 50 + i * 40), staging: deployed(b.name, version, 40 + i * 40), prod: deployed(b.name, version, 30 + i * 40) },
      }))
      state.graphs[b.name] = {
        nodes: ['test', 'staging', 'prod'].map((e) => step(`${name}-${e}`, e, e === 'prod' && i % 5 === 2 ? 'WaitingForMerge' : 'Verified')),
        edges: [{ from: `step-${name}-test`, to: `step-${name}-staging` }, { from: `step-${name}-staging`, to: `step-${name}-prod` }],
      }
    }
  }
  return state
}

/** The UI API over fixtures, as a Playwright route handler for context.route('**\/api/v1/ui/**'). */
export function apiHandler(state) {
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
  return async (route) => {
    const req = route.request()
    const url = new URL(req.url())
    const parts = url.pathname.split('/').slice(4).map(decodeURIComponent) // after /api/v1/ui/
    if (req.method() === 'GET') {
      if (parts[0] === 'pipelines' && parts.length === 1) return json(route, state.pipelines)
      if (parts[0] === 'gates') return json(route, state.gates)
      if (parts[0] === 'pipelines' && parts[2] === 'bundles') return json(route, state.bundles[parts[1]] ?? [])
      if (parts[0] === 'bundles' && parts[2] === 'graph') return json(route, state.graphs[parts[1]] ?? { nodes: [], edges: [] })
      if (parts[0] === 'bundles' && parts[2] === 'steps') return json(route, state.steps[parts[1]] ?? [])
      if (parts[0] === 'steps' && parts[3] === 'events') return json(route, [])
      return json(route, { error: `no fixture for GET ${url.pathname}` }, 404)
    }
    const body = req.postDataJSON() ?? {}
    switch (parts[0]) {
      case 'approvals': return json(route, approve(state, body))
      case 'rollback': return json(route, rollback(state, body))
      case 'validate-cel': return json(route, { valid: true, expression: body.expression })
      case 'override': return json(route, override(state, body))
      case 'pause': return json(route, { message: `paused ${body.pipeline}` })
      case 'resume': return json(route, { message: `resumed ${body.pipeline}` })
      case 'promote': return json(route, { bundle: 'promote-bundle', message: `promoting ${body.pipeline} to ${body.environment}` })
      case 'bundles': return json(route, { bundle: `${body.pipeline}-new`, message: 'version shipped' })
    }
    return json(route, { error: `no fixture for ${req.method()} ${url.pathname}` }, 404)
  }
}

/** An approval counts; with two the gate passes and prod-eu starts promoting. */
function approve(state, { bundle: bundleName, environment, decision = 'approve' }) {
  const g = state.gates.find((x) => x.bundle === bundleName && x.environment === environment && x.approval)
  if (g && decision === 'approve') {
    g.approval.approved += 1
    g.approval.decisions.push({ user: 'you', decision: 'approve', counted: true, firstSeenAt: new Date(NOW).toISOString() })
    if (g.approval.approved >= g.approval.required) {
      Object.assign(g, { ready: true, holding: false, state: 'Pass', reason: `approved: 2 of 2 (alice, you)` })
      const graph = state.graphs[bundleName]
      for (const n of graph?.nodes ?? []) {
        if (n.id === 'gate-release-approval') Object.assign(n, { state: 'Pass', holding: false, message: g.reason })
        if (n.id === 'step-co-prod-eu') Object.assign(n, { state: 'Promoting', startedAt: new Date(NOW).toISOString() })
      }
      const p = state.pipelines.find((x) => x.name === g.pipeline)
      if (p) {
        p.blockerCount = Math.max(0, (p.blockerCount ?? 1) - 1)
        p.environmentStates = { ...p.environmentStates, [environment]: 'Promoting' }
        p.summary = summarize([['1.8.0', 'live', ['test', 'uat']], ['1.8.0', 'promoting', ['prod-eu']], ['1.8.0', 'held by gate uat-soak', ['prod-us']]])
      }
    }
  }
  return { outcome: 'Recorded', approval: 'approval-1', user: 'you', message: `Recorded: you ${decision} ${bundleName} for ${environment}` }
}

/** A rollback ships the previous version to the environment, which starts promoting it; with a reason it holds the environment there. */
function rollback(state, { pipeline: name, environment, holdReason }) {
  const p = state.pipelines.find((x) => x.name === name)
  const graph = p && state.graphs[p.activeBundleName]
  const node = graph?.nodes.find((n) => n.type === 'PromotionStep' && n.environment === environment)
  const rollbackName = `${name}-rollback-${environment}`
  if (node) Object.assign(node, { state: 'Promoting', startedAt: new Date(NOW).toISOString() })
  if (p) {
    p.environmentStates = { ...p.environmentStates, [environment]: 'Promoting' }
    if (holdReason) {
      p.environmentTopology = p.environmentTopology.map((e) => e.name === environment
        ? { ...e, hold: { bundle: rollbackName, reason: holdReason, createdBy: 'you', createdAt: new Date(NOW).toISOString() } }
        : e)
    }
  }
  return { bundle: rollbackName, held: !!holdReason, message: `Rolling back ${environment} of ${name} to the previous verified version` }
}

/** An override lets the environment pass a gate for a while; the held step starts promoting. */
function override(state, { pipeline: name, gate: gateName, environment, reason, expiresInMinutes = 60 }) {
  const g = state.gates.find((x) => x.pipeline === name && x.environment === environment && (x.gateTemplate === gateName || x.name === gateName))
  const expiresAt = new Date(NOW + expiresInMinutes * MIN).toISOString()
  if (g) {
    Object.assign(g, { ready: true, holding: false, state: 'Pass', reason: `overridden by you: ${reason}`, overrides: [{ reason, expiresAt }] })
    const p = state.pipelines.find((x) => x.name === name)
    for (const n of state.graphs[g.bundle]?.nodes ?? []) {
      if (n.type === 'PolicyGate' && n.environment === environment) Object.assign(n, { state: 'Pass', holding: false, message: g.reason })
      if (n.type === 'PromotionStep' && n.environment === environment) Object.assign(n, { state: 'Promoting', startedAt: new Date(NOW).toISOString() })
    }
    if (p) {
      p.blockerCount = Math.max(0, (p.blockerCount ?? 1) - 1)
      p.environmentStates = { ...p.environmentStates, [environment]: 'Promoting' }
    }
  }
  return { message: `Overridden: ${environment} passes ${gateName} until ${expiresAt}`, gates: g ? [g.name] : [], expiresAt }
}
