// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// mock-server/server.mjs — Lightweight HTTP mock server for E2E tests.
//
// Serves deterministic fixture data for the 8 baseline journeys.
// No real Kubernetes cluster required.
//
// Routes:
//   GET  /api/v1/ui/pipelines           → pipeline list fixture
//   GET  /api/v1/ui/pipelines/:name/bundles → bundles fixture
//   GET  /api/v1/ui/bundles/:name/graph  → graph fixture
//   GET  /api/v1/ui/bundles/:name/steps  → steps fixture
//   GET  /api/v1/ui/gates               → gates fixture
//   POST /api/v1/ui/pause               → { message: "paused" }
//   POST /api/v1/ui/resume              → { message: "resumed" }
//   POST /api/v1/ui/promote             → { bundle: "...", message: "ok" }
//   POST /api/v1/ui/validate-cel        → { valid: true }
//   GET  /ui/*                          → serves built static assets (Vite build)
//   GET  /logo.png                      → placeholder 1x1 PNG

import http from 'node:http'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
// DIST path: server.mjs is at web/test/e2e/mock-server/server.mjs
// web/dist/ is three levels up from this file's directory
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
    cdLevel: 'mostly-cd',
    blockerCount: 0,
    failedStepCount: 0,
  },
  {
    name: 'payments-service',
    namespace: 'default',
    phase: 'Unknown',
    environmentCount: 2,
    activeBundleName: 'payments-service-def456',
    environmentStates: { staging: 'Promoting', prod: 'Pending' },
    cdLevel: 'manual',
    blockerCount: 2,
    failedStepCount: 0,
  },
]

const BUNDLES = {
  'kardinal-test-app': [
    {
      name: 'kardinal-test-app-abc123',
      namespace: 'default',
      phase: 'Promoting',
      type: 'standard',
      pipeline: 'kardinal-test-app',
      createdAt: new Date(Date.now() - 600_000).toISOString(),
      provenance: { author: 'ci-bot', commitSHA: 'abc1234567890', ciRunURL: 'https://github.com/org/repo/runs/1' },
    },
    {
      name: 'kardinal-test-app-prev111',
      namespace: 'default',
      phase: 'Superseded',
      type: 'standard',
      pipeline: 'kardinal-test-app',
      createdAt: new Date(Date.now() - 7_200_000).toISOString(),
    },
  ],
  'payments-service': [
    {
      name: 'payments-service-def456',
      namespace: 'default',
      phase: 'Promoting',
      type: 'standard',
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
      { id: 'gate-no-weekend', type: 'PolicyGate', label: 'no-weekend-deploys', environment: 'no-weekend-deploys', state: 'Block', expression: '!schedule.isWeekend()', lastEvaluatedAt: new Date(Date.now() - 30_000).toISOString() },
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
    { name: 'step-test-abc', namespace: 'default', pipeline: 'kardinal-test-app', bundle: 'kardinal-test-app-abc123', environment: 'test', stepType: 'standard', state: 'Verified', currentStepIndex: 7, conditions: [{ type: 'Ready', status: 'True', message: 'All steps complete' }] },
    { name: 'step-prod-abc', namespace: 'default', pipeline: 'kardinal-test-app', bundle: 'kardinal-test-app-abc123', environment: 'prod', stepType: 'standard', state: 'WaitingForMerge', prURL: 'https://github.com/org/repo/pull/42', currentStepIndex: 5 },
  ],
}

const GATES = [
  { name: 'no-weekend-deploys', namespace: 'default', expression: '!schedule.isWeekend()', ready: false, reason: 'Today is a weekend', lastEvaluatedAt: new Date(Date.now() - 30_000).toISOString() },
  { name: 'business-hours', namespace: 'default', expression: 'schedule.isBusinessHours()', ready: true, lastEvaluatedAt: new Date(Date.now() - 10_000).toISOString() },
]

// ── Helpers ───────────────────────────────────────────────────────────────────

function json(res, data, status = 200) {
  res.writeHead(status, { 'Content-Type': 'application/json', 'Access-Control-Allow-Origin': '*' })
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
    res.writeHead(200, { 'Content-Type': mime })
    res.end(content)
  } catch {
    res.writeHead(404)
    res.end('Not found')
  }
}

// Minimal 1×1 transparent PNG for /logo.png
const LOGO_PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==', 'base64')

// ── Server ────────────────────────────────────────────────────────────────────

const server = http.createServer(async (req, res) => {
  const url = req.url ?? '/'
  const method = req.method ?? 'GET'

  // CORS preflight
  if (method === 'OPTIONS') {
    res.writeHead(204, { 'Access-Control-Allow-Origin': '*', 'Access-Control-Allow-Methods': 'GET,POST', 'Access-Control-Allow-Headers': 'Content-Type' })
    res.end(); return
  }

  // ── API routes ──────────────────────────────────────────────────────────────
  if (url === '/api/v1/ui/pipelines' && method === 'GET') {
    return json(res, PIPELINES)
  }
  if (url === '/api/v1/ui/gates' && method === 'GET') {
    return json(res, GATES)
  }
  if (url.startsWith('/api/v1/ui/pipelines/') && url.endsWith('/bundles') && method === 'GET') {
    const name = url.replace('/api/v1/ui/pipelines/', '').replace('/bundles', '')
    return json(res, BUNDLES[name] ?? [])
  }
  if (url.startsWith('/api/v1/ui/bundles/') && url.endsWith('/graph') && method === 'GET') {
    const name = url.replace('/api/v1/ui/bundles/', '').replace('/graph', '')
    return json(res, GRAPHS[name] ?? { nodes: [], edges: [] })
  }
  if (url.startsWith('/api/v1/ui/bundles/') && url.endsWith('/steps') && method === 'GET') {
    const name = url.replace('/api/v1/ui/bundles/', '').replace('/steps', '')
    return json(res, STEPS[name] ?? [])
  }
  if (url === '/api/v1/ui/pause' && method === 'POST') {
    await readBody(req)
    return json(res, { message: 'paused' })
  }
  if (url === '/api/v1/ui/resume' && method === 'POST') {
    await readBody(req)
    return json(res, { message: 'resumed' })
  }
  if (url === '/api/v1/ui/promote' && method === 'POST') {
    await readBody(req)
    return json(res, { bundle: 'new-bundle', message: 'promotion started' })
  }
  if (url === '/api/v1/ui/rollback' && method === 'POST') {
    await readBody(req)
    return json(res, { bundle: 'rollback-bundle', message: 'rollback started' })
  }
  if (url === '/api/v1/ui/validate-cel' && method === 'POST') {
    const body = await readBody(req)
    return json(res, { valid: true, expression: body.expression })
  }

  // ── Static assets ───────────────────────────────────────────────────────────
  if (url === '/logo.png') {
    res.writeHead(200, { 'Content-Type': 'image/png' })
    res.end(LOGO_PNG); return
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
