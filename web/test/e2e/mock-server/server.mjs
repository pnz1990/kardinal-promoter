// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// mock-server/server.mjs — Lightweight HTTP mock server for E2E tests.
//
// Serves deterministic fixture data for the journeys in ../journeys/ (001-011).
// No real Kubernetes cluster required. Responses use the same shapes as
// cmd/kardinal-controller/ui_api.go, and only /ui/* is served as static files,
// like the real controller (so a UI asset loaded from the wrong path fails here too).
//
// Routes (query strings are parsed, not matched):
//   GET  /api/v1/ui/pipelines                         → pipeline list fixture
//   GET  /api/v1/ui/pipelines/:name/bundles[?namespace=] → bundles fixture, filtered by namespace
//   GET  /api/v1/ui/bundles/:name/graph               → graph fixture
//   GET  /api/v1/ui/bundles/:name/steps               → steps fixture
//   GET  /api/v1/ui/gates                             → gate instances + one template
//   GET  /api/v1/ui/steps/:ns/:name/events            → [] (no Events)
//   POST /api/v1/ui/bundles                           → { bundle: "new-bundle", message }
//   POST /api/v1/ui/pause                             → { message: "paused" }
//   POST /api/v1/ui/resume                            → { message: "resumed" }
//   POST /api/v1/ui/promote                           → { bundle: "new-bundle", message }
//   POST /api/v1/ui/rollback                          → { bundle: "rollback-bundle", message }
//   POST /api/v1/ui/approvals                         → { outcome, approval, user: "carol", message }
//   POST /api/v1/ui/reject, /override, /release-hold  → { message } (override also { gates, expiresAt })
//   POST /api/v1/ui/validate-cel                      → { valid: true }
//   GET  /                                            → 302 to /ui/
//   GET  /ui/*                                        → built static assets (web/dist), SPA fallback

import http from 'node:http'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

// Every time the mock serves is "so long ago", resolved when a response is sent:
// computed once at load, a gate evaluated "30s ago" grew older the longer the server
// ran, and checks that read ages (a stale gate, a freshness label) became flaky.
const AGO = Symbol('ago')
/** A timestamp ms before the moment the response is sent. */
const ago = ms => ({ [AGO]: ms })
/** data with each ago() resolved against now. */
function resolveTimes(data, now = Date.now()) {
  if (Array.isArray(data)) return data.map(v => resolveTimes(v, now))
  if (data && typeof data === 'object') {
    if (AGO in data) return new Date(now - data[AGO]).toISOString()
    return Object.fromEntries(Object.entries(data).map(([k, v]) => [k, resolveTimes(v, now)]))
  }
  return data
}

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const DIST = path.join(__dirname, '../../../dist')
const PORT = parseInt(process.env.KARDINAL_E2E_PORT ?? '3001', 10)
// Loopback only: the mock server serves fixtures to the local browser and nothing else.
const HOST = process.env.KARDINAL_E2E_HOST ?? '127.0.0.1'

// ── Fixtures ─────────────────────────────────────────────────────────────────

const PIPELINES = [
  {
    name: 'kardinal-test-app',
    namespace: 'default',
    phase: 'Ready',
    environmentCount: 3,
    activeBundleName: 'kardinal-test-app-abc123',
    environmentStates: { test: 'Verified', uat: 'Verified', prod: 'WaitingForMerge' },
    blockerCount: 0,
    failedStepCount: 0,
    activeBundleVersion: 'sha-abc1234',
    environmentTopology: [{ name: 'test' }, { name: 'uat', upstreams: ['test'] }, { name: 'prod', upstreams: ['uat'], approval: 'pr-review' }],
    deployed: {
      test: { bundle: 'kardinal-test-app-abc123', version: 'sha-abc1234', verifiedAt: ago(420_000) },
      uat: { bundle: 'kardinal-test-app-abc123', version: 'sha-abc1234', verifiedAt: ago(240_000), configFrom: 'kardinal-test-app-cfg9', configVersion: 'config 77aa001' },
      prod: { bundle: 'kardinal-test-app-prev111', version: 'sha-9f8e7d6', verifiedAt: ago(7_000_000) },
    },
    // Pipeline status.environments and status.summary (P1, served since #1727).
    environments: [
      { name: 'test', bundle: 'kardinal-test-app-abc123', version: 'sha-abc1234', state: 'Verified', since: new Date(Date.now() - 420_000).toISOString() },
      { name: 'uat', bundle: 'kardinal-test-app-abc123', version: 'sha-abc1234', state: 'Verified', since: new Date(Date.now() - 240_000).toISOString() },
      { name: 'prod', bundle: 'kardinal-test-app-prev111', version: 'sha-9f8e7d6', incomingBundle: 'kardinal-test-app-abc123', incomingVersion: 'sha-abc1234',
        state: 'WaitingForMerge', since: new Date(Date.now() - 300_000).toISOString(), blockedBy: ['no-weekend-deploys'], prURL: 'https://github.com/pnz1990/kardinal-demo/pull/42' },
    ],
    summary: 'sha-abc1234 live in test, uat; held by gate no-weekend-deploys in prod (#42)',
  },
  {
    name: 'payments-service',
    namespace: 'default',
    phase: 'Unknown',
    environmentCount: 2,
    activeBundleName: 'payments-service-def456',
    environmentStates: { staging: 'Promoting', prod: 'Pending' },
    blockerCount: 2,
    failedStepCount: 0,
    activeBundleVersion: '6.14.1',
    environmentTopology: [{ name: 'staging' }, { name: 'prod', upstreams: ['staging'] }],
    // status.conditions (#1727, #1506): its git Secret is not labelled referenceable.
    conditions: [
      { type: 'Ready', status: 'False', reason: 'SecretNotReferenceable',
        message: 'git Secret github-token is not labeled kardinal.io/referenceable=true, so git does not use it; label it: kubectl label secret github-token -n default kardinal.io/referenceable=true' },
    ],
  },
]

const BUNDLES = {
  'kardinal-test-app': [
    {
      name: 'kardinal-test-app-abc123',
      namespace: 'default',
      phase: 'Promoting',
      type: 'image',
      version: 'sha-abc1234',
      pipeline: 'kardinal-test-app',
      createdAt: ago(600_000),
      provenance: { author: 'ci-bot', commitSHA: 'abc1234567890', ciRunURL: 'https://github.com/org/repo/runs/1' },
    },
    {
      name: 'kardinal-test-app-prev111',
      namespace: 'default',
      phase: 'Superseded',
      type: 'image',
      version: 'sha-9f8e7d6',
      pipeline: 'kardinal-test-app',
      createdAt: ago(7_200_000),
    },
    {
      name: 'kardinal-test-app-cfg9',
      namespace: 'default',
      phase: 'Verified',
      type: 'config',
      version: 'config 77aa001',
      pipeline: 'kardinal-test-app',
      createdAt: ago(3_600_000),
    },
  ],
  'payments-service': [
    {
      name: 'payments-service-def456',
      namespace: 'default',
      phase: 'Promoting',
      type: 'mixed',
      version: '6.14.1',
      pipeline: 'payments-service',
      createdAt: ago(120_000),
    },
    {
      name: 'payments-service-rej77',
      namespace: 'default',
      phase: 'Rejected',
      type: 'image',
      pipeline: 'payments-service',
      createdAt: ago(900_000),
      rejected: { by: 'bob', reason: 'CVE-2026-1234 in the base image', at: ago(300_000) },
    },
  ],
}

const GRAPHS = {
  'kardinal-test-app-abc123': {
    nodes: [
      { id: 'step-test', type: 'PromotionStep', label: 'test', environment: 'test', state: 'Verified', startedAt: ago(580_000) },
      { id: 'step-uat', type: 'PromotionStep', label: 'uat', environment: 'uat', state: 'Verified', startedAt: ago(400_000) },
      // holding: the UI API sets it with graph.GateHolds; the UI counts only
      // holding gates as blocked (E2E-R19). The mock states it, not the rule.
      { id: 'gate-no-weekend', type: 'PolicyGate', label: 'no-weekend-deploys', environment: 'prod', state: 'Block', holding: true, expression: '!schedule.isWeekend', lastEvaluatedAt: ago(30_000) },
      { id: 'step-prod', type: 'PromotionStep', label: 'prod', environment: 'prod', state: 'WaitingForMerge', prURL: 'https://github.com/org/repo/pull/42', startedAt: ago(200_000) },
    ],
    edges: [
      { from: 'step-test', to: 'step-uat' },
      { from: 'step-uat', to: 'gate-no-weekend' },
      { from: 'gate-no-weekend', to: 'step-prod' },
    ],
  },
  'payments-service-def456': {
    nodes: [
      { id: 'step-staging', type: 'PromotionStep', label: 'staging', environment: 'staging', state: 'Promoting', startedAt: ago(60_000) },
      { id: 'step-prod', type: 'PromotionStep', label: 'prod', environment: 'prod', state: 'Pending' },
    ],
    edges: [{ from: 'step-staging', to: 'step-prod' }],
  },
}

const STEPS = {
  'kardinal-test-app-abc123': [
    { name: 'step-test-abc', namespace: 'default', pipeline: 'kardinal-test-app', bundle: 'kardinal-test-app-abc123', environment: 'test', stepType: 'kustomize-set-image', state: 'Verified', currentStepIndex: 7, conditions: [{ type: 'Ready', status: 'True', message: 'All steps complete' }],
      steps: [
        ['git-clone', 0, 2400], ['kustomize-set-image', 2400, 300], ['git-commit', 2700, 200], ['git-push', 2900, 1800], ['health-check', 4700, 41000],
      ].map(([name, from, ms]) => ({ name, state: 'Completed', startedAt: ago(580_000 - from),
        completedAt: ago(580_000 - from - ms), durationMs: ms })) },
    { name: 'step-prod-abc', namespace: 'default', pipeline: 'kardinal-test-app', bundle: 'kardinal-test-app-abc123', environment: 'prod', stepType: 'kustomize-set-image', state: 'WaitingForMerge', prURL: 'https://github.com/org/repo/pull/42', currentStepIndex: 5 },
  ],
}

// Gate instances belong to one bundle (ui_api.go uiGateResponse: pipeline, bundle,
// environment). The template is what the user wrote; the UI must not count it.
// state and holding are what graph.GateState and graph.GateHolds give
// them; only a Block (holding) gate counts as blocked (E2E-R19).
const GATES = [
  { name: 'no-weekend-deploys-kardinal-test-app-abc123-prod', namespace: 'default', pipeline: 'kardinal-test-app', bundle: 'kardinal-test-app-abc123', environment: 'prod', gateTemplate: 'no-weekend-deploys', maxOverrideMinutes: 1440, expression: '!schedule.isWeekend', ready: false, holding: true, state: 'Block', reason: 'Today is a weekend', lastEvaluatedAt: ago(30_000) },
  { name: 'business-hours-kardinal-test-app-abc123-prod', namespace: 'default', pipeline: 'kardinal-test-app', bundle: 'kardinal-test-app-abc123', environment: 'prod', gateTemplate: 'business-hours', maxOverrideMinutes: 1440, expression: 'schedule.hour >= 9 && schedule.hour < 17', ready: true, state: 'Pass', lastEvaluatedAt: ago(10_000) },
  { name: 'no-weekend-deploys', namespace: 'default', expression: '!schedule.isWeekend', ready: false, template: true, state: 'Pending' },
  {
    name: 'release-approval-payments-service-def456-prod', namespace: 'default', pipeline: 'payments-service', bundle: 'payments-service-def456', environment: 'prod',
    gateTemplate: 'release-approval', maxOverrideMinutes: 1440, createdAt: ago(1_500_000), expression: 'true', ready: false, holding: true, state: 'Block', reason: 'waiting for approvals: 1 of 2 (alice)', lastEvaluatedAt: ago(20_000),
    approval: {
      required: 2, allowedGroups: ['release-managers'], excludeAuthor: true, approved: 1,
      decisions: [
        { user: 'alice', decision: 'approve', counted: true, comment: 'canary looks clean', firstSeenAt: ago(3_600_000) },
        { user: 'ci-bot', decision: 'approve', counted: false, reason: 'the version\'s author (approval.excludeAuthor)', firstSeenAt: ago(600_000) },
      ],
    },
  },
]

// ── Helpers ───────────────────────────────────────────────────────────────────

// The headers the controller's UI server sends (cmd/kardinal-controller/
// ui_security_headers.go), so every journey runs under the same CSP.
const SECURITY_HEADERS = {
  'Content-Security-Policy': "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'",
  'X-Frame-Options': 'DENY',
  'X-Content-Type-Options': 'nosniff',
  'Referrer-Policy': 'no-referrer',
}

function json(res, data, status = 200) {
  res.writeHead(status, { 'Content-Type': 'application/json', 'Access-Control-Allow-Origin': '*', ...SECURITY_HEADERS })
  res.end(JSON.stringify(resolveTimes(data)))
}

function readBody(req) {
  return new Promise(resolve => {
    let body = ''
    req.on('data', c => { body += c })
    req.on('end', () => {
      try { resolve(JSON.parse(body)) } catch { resolve({}) }
    })
  })
}

function serveFile(res, filePath) {
  try {
    const content = fs.readFileSync(filePath)
    const ext = path.extname(filePath)
    const mime = {
      '.html': 'text/html', '.js': 'application/javascript',
      '.css': 'text/css', '.png': 'image/png', '.svg': 'image/svg+xml', '.woff2': 'font/woff2',
    }[ext] ?? 'application/octet-stream'
    res.writeHead(200, { 'Content-Type': mime, ...SECURITY_HEADERS })
    res.end(content)
  } catch {
    res.writeHead(404)
    res.end('Not found')
  }
}

// ── Server ────────────────────────────────────────────────────────────────────

const server = http.createServer(async (req, res) => {
  const parsed = new URL(req.url ?? '/', 'http://localhost')
  const url = parsed.pathname
  const method = req.method ?? 'GET'

  // CORS preflight
  if (method === 'OPTIONS') {
    res.writeHead(204, { 'Access-Control-Allow-Origin': '*', 'Access-Control-Allow-Methods': 'GET,POST', 'Access-Control-Allow-Headers': 'Content-Type' })
    res.end(); return
  }

  // ── API routes ──────────────────────────────────────────────────────────────
  const parts = url.split('/').map(p => decodeURIComponent(p)) // ['', 'api', 'v1', 'ui', ...]
  const api = url.startsWith('/api/v1/ui/') ? parts.slice(4) : null

  if (api && method === 'GET') {
    if (api.length === 1 && api[0] === 'pipelines') return json(res, PIPELINES)
    if (api.length === 1 && api[0] === 'gates') return json(res, GATES)
    if (api.length === 3 && api[0] === 'pipelines' && api[2] === 'bundles') {
      const ns = parsed.searchParams.get('namespace')
      const bundles = (BUNDLES[api[1]] ?? []).filter(b => !ns || b.namespace === ns)
      return json(res, bundles)
    }
    if (api.length === 3 && api[0] === 'bundles' && api[2] === 'graph') {
      return json(res, GRAPHS[api[1]] ?? { nodes: [], edges: [] })
    }
    if (api.length === 3 && api[0] === 'bundles' && api[2] === 'steps') {
      return json(res, STEPS[api[1]] ?? [])
    }
    if (api.length === 4 && api[0] === 'steps' && api[3] === 'events') {
      return json(res, [])
    }
  }
  if (api && method === 'POST' && api.length === 1) {
    const body = await readBody(req)
    switch (api[0]) {
      case 'bundles': return json(res, { bundle: 'new-bundle', message: 'bundle created' })
      case 'pause': return json(res, { message: 'paused' })
      case 'resume': return json(res, { message: 'resumed' })
      case 'promote': return json(res, { bundle: 'new-bundle', message: 'promotion started' })
      case 'rollback': return json(res, { bundle: 'rollback-bundle', message: 'rollback started' })
      case 'approvals': return json(res, {
        outcome: body.revoke ? 'Revoked' : 'Recorded', approval: 'approval-1', user: 'carol',
        message: body.revoke ? `Revoked: carol no longer approves ${body.bundle} for ${body.environment}`
          : `Recorded: carol ${body.decision}s ${body.bundle} for ${body.environment}`,
      })
      case 'validate-cel': return json(res, { valid: true, expression: body.expression })
      case 'reject': return json(res, { message: `bundle ${body.bundle} rejected by carol: it is never promoted again` })
      case 'override': return json(res, { message: `gate ${body.gate} overridden in ${body.environment}`, gates: [body.gate],
        expiresAt: new Date(Date.now() + (body.expiresInMinutes ?? 60) * 60_000).toISOString() })
      case 'release-hold': return json(res, { message: 'hold released' })
      // U7: who the caller is and what they may do (ui_access.go). The bearer token picks
      // the caller: none is carol, an approver in release-managers who may do everything;
      // "viewer" is victor, who may only read; "open" is a UI without TokenReview (no user,
      // everything allowed). Read from the request, so parallel journeys do not share state.
      case 'access': {
        const auth = req.headers.authorization ?? ''
        const viewer = auth === 'Bearer viewer'
        const open = auth === 'Bearer open'
        const roles = {
          'update pipelines/pause': 'promoter or admin', 'update pipelines/hold': 'promoter or admin',
          'update bundles/reject': 'promoter, approver or admin', 'update policygates/override': 'approver or admin',
          'create bundles': 'promoter or admin', 'create approvals': 'approver or admin',
        }
        const results = (body.checks ?? []).map(c => {
          const approving = c.verb === 'create' && c.resource === 'approvals'
          // Without TokenReview an approval cannot name who decided (ui_access.go).
          if (open && approving) return { allowed: false, grantedBy: roles['create approvals'],
            reason: 'approving needs sign-in with your Kubernetes identity (the UI\'s TokenReview mode); kardinal approve works without it' }
          return { allowed: !viewer || c.verb === 'get' || c.verb === 'list', grantedBy: roles[`${c.verb} ${c.resource}${c.subresource ? `/${c.subresource}` : ''}`] }
        })
        const user = open ? undefined : viewer ? { username: 'victor', groups: ['viewers'] } : { username: 'carol', groups: ['release-managers'] }
        return json(res, { ...(user ? { user } : {}), results })
      }
    }
  }
  if (api) return json(res, { error: `no mock for ${method} ${url}` }, 404)

  // ── Static assets ───────────────────────────────────────────────────────────
  // Redirect root to the UI so tests can use page.goto('/') with baseURL set to the origin.
  if (url === '/' || url === '') {
    res.writeHead(302, { Location: '/ui/' })
    res.end(); return
  }
  if (url.startsWith('/ui/')) {
    const assetPath = url.replace('/ui/', '')
    if (assetPath === '' || assetPath === 'index.html') {
      return serveFile(res, path.join(DIST, 'index.html'))
    }
    const full = path.join(DIST, assetPath)
    if (fs.existsSync(full)) return serveFile(res, full)
    // SPA fallback
    return serveFile(res, path.join(DIST, 'index.html'))
  }

  res.writeHead(404)
  res.end('Not found')
})

server.listen(PORT, HOST, () => {
  console.log(`[mock-server] Kardinal E2E mock server running on http://${HOST}:${PORT}`)
  console.log(`[mock-server] UI available at http://${HOST}:${PORT}/ui/`)
})
