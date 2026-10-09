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
//   POST /api/v1/ui/pause                             → { message: "paused" }
//   POST /api/v1/ui/resume                            → { message: "resumed" }
//   POST /api/v1/ui/promote                           → { bundle: "new-bundle", message }
//   POST /api/v1/ui/rollback                          → { bundle: "rollback-bundle", message }
//   POST /api/v1/ui/validate-cel                      → { valid: true }
//   GET  /                                            → 302 to /ui/
//   GET  /ui/*                                        → built static assets (web/dist), SPA fallback

import http from 'node:http'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const DIST = path.join(__dirname, '../../../dist')
const PORT = parseInt(process.env.KARDINAL_E2E_PORT ?? '3001', 10)

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
      test: { bundle: 'kardinal-test-app-abc123', version: 'sha-abc1234', verifiedAt: new Date(Date.now() - 420_000).toISOString() },
      uat: { bundle: 'kardinal-test-app-abc123', version: 'sha-abc1234', verifiedAt: new Date(Date.now() - 240_000).toISOString(), configFrom: 'kardinal-test-app-cfg9', configVersion: 'config 77aa001' },
      prod: { bundle: 'kardinal-test-app-prev111', version: 'sha-9f8e7d6', verifiedAt: new Date(Date.now() - 7_000_000).toISOString() },
    },
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
    activeBundleVersion: 'v2.4.0 with config 1a2b3c4',
    environmentTopology: [{ name: 'staging' }, { name: 'prod', upstreams: ['staging'] }],
  },
]

const BUNDLES = {
  'kardinal-test-app': [
    {
      name: 'kardinal-test-app-abc123',
      namespace: 'default',
      phase: 'Promoting',
      type: 'image',
      pipeline: 'kardinal-test-app',
      createdAt: new Date(Date.now() - 600_000).toISOString(),
      provenance: { author: 'ci-bot', commitSHA: 'abc1234567890', ciRunURL: 'https://github.com/org/repo/runs/1' },
    },
    {
      name: 'kardinal-test-app-prev111',
      namespace: 'default',
      phase: 'Superseded',
      type: 'image',
      pipeline: 'kardinal-test-app',
      createdAt: new Date(Date.now() - 7_200_000).toISOString(),
    },
    {
      name: 'kardinal-test-app-cfg9',
      namespace: 'default',
      phase: 'Verified',
      type: 'config',
      pipeline: 'kardinal-test-app',
      createdAt: new Date(Date.now() - 3_600_000).toISOString(),
    },
  ],
  'payments-service': [
    {
      name: 'payments-service-def456',
      namespace: 'default',
      phase: 'Promoting',
      type: 'mixed',
      pipeline: 'payments-service',
      createdAt: new Date(Date.now() - 120_000).toISOString(),
    },
  ],
}

const GRAPHS = {
  'kardinal-test-app-abc123': {
    nodes: [
      { id: 'step-test', type: 'PromotionStep', label: 'test', environment: 'test', state: 'Verified', startedAt: new Date(Date.now() - 580_000).toISOString() },
      { id: 'step-uat', type: 'PromotionStep', label: 'uat', environment: 'uat', state: 'Verified', startedAt: new Date(Date.now() - 400_000).toISOString() },
      // holding: the UI API sets it with graph.GateHolds; the UI counts only
      // holding gates as blocked (E2E-R19). The mock states it, not the rule.
      { id: 'gate-no-weekend', type: 'PolicyGate', label: 'no-weekend-deploys', environment: 'no-weekend-deploys', state: 'Block', holding: true, expression: '!schedule.isWeekend', lastEvaluatedAt: new Date(Date.now() - 30_000).toISOString() },
      { id: 'step-prod', type: 'PromotionStep', label: 'prod', environment: 'prod', state: 'WaitingForMerge', prURL: 'https://github.com/org/repo/pull/42', startedAt: new Date(Date.now() - 200_000).toISOString() },
    ],
    edges: [
      { from: 'step-test', to: 'step-uat' },
      { from: 'step-uat', to: 'gate-no-weekend' },
      { from: 'gate-no-weekend', to: 'step-prod' },
    ],
  },
  'payments-service-def456': {
    nodes: [
      { id: 'step-staging', type: 'PromotionStep', label: 'staging', environment: 'staging', state: 'Promoting', startedAt: new Date(Date.now() - 60_000).toISOString() },
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
      ].map(([name, from, ms]) => ({ name, state: 'Completed', startedAt: new Date(Date.now() - 580_000 + from).toISOString(),
        completedAt: new Date(Date.now() - 580_000 + from + ms).toISOString(), durationMs: ms })) },
    { name: 'step-prod-abc', namespace: 'default', pipeline: 'kardinal-test-app', bundle: 'kardinal-test-app-abc123', environment: 'prod', stepType: 'kustomize-set-image', state: 'WaitingForMerge', prURL: 'https://github.com/org/repo/pull/42', currentStepIndex: 5 },
  ],
}

// Gate instances belong to one bundle (ui_api.go uiGateResponse: pipeline, bundle,
// environment). The template is what the user wrote; the UI must not count it.
// state and holding are what graph.GateState and graph.GateHolds give
// them; only a Block (holding) gate counts as blocked (E2E-R19).
const GATES = [
  { name: 'no-weekend-deploys-kardinal-test-app-abc123-prod', namespace: 'default', pipeline: 'kardinal-test-app', bundle: 'kardinal-test-app-abc123', environment: 'prod', expression: '!schedule.isWeekend', ready: false, holding: true, state: 'Block', reason: 'Today is a weekend', lastEvaluatedAt: new Date(Date.now() - 30_000).toISOString() },
  { name: 'business-hours-kardinal-test-app-abc123-prod', namespace: 'default', pipeline: 'kardinal-test-app', bundle: 'kardinal-test-app-abc123', environment: 'prod', expression: 'schedule.hour >= 9 && schedule.hour < 17', ready: true, state: 'Pass', lastEvaluatedAt: new Date(Date.now() - 10_000).toISOString() },
  { name: 'no-weekend-deploys', namespace: 'default', expression: '!schedule.isWeekend', ready: false, template: true, state: 'Pending' },
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
  res.end(JSON.stringify(data))
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
      '.css': 'text/css', '.png': 'image/png', '.svg': 'image/svg+xml',
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
  }
  if (api && method === 'POST' && api.length === 1) {
    const body = await readBody(req)
    switch (api[0]) {
      case 'pause': return json(res, { message: 'paused' })
      case 'resume': return json(res, { message: 'resumed' })
      case 'promote': return json(res, { bundle: 'new-bundle', message: 'promotion started' })
      case 'rollback': return json(res, { bundle: 'rollback-bundle', message: 'rollback started' })
      case 'validate-cel': return json(res, { valid: true, expression: body.expression })
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

server.listen(PORT, () => {
  console.log(`[mock-server] Kardinal E2E mock server running on http://localhost:${PORT}`)
  console.log(`[mock-server] UI available at http://localhost:${PORT}/ui/`)
})
