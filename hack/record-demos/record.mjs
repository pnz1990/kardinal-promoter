#!/usr/bin/env node
// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// record.mjs — records the docs' UI demo clips (docs/assets/demos/<flow>[-dark].webp,
// with a <flow>[-dark].png poster) against the mock server and the fixtures.
//
//   cd web && npm ci && npm run build          # the UI the clips show (web/dist)
//   cd hack/record-demos && npm ci
//   npx playwright install chromium            # once
//   node record.mjs                            # every flow, into docs/assets/demos
//   node record.mjs --only first-look,palette  # some flows
//   node record.mjs --check                    # record into a temp dir and check
//                                              # each clip exists and fits the budget
//
// The mock server (web/test/e2e/mock-server) serves web/dist under the controller's
// CSP; the page's /api/v1/ui/* requests are answered from fixtures.mjs, and its clock
// starts at fixtures NOW, so the clips are the same on every run.
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'
import { apiHandler, createFixtures, NOW } from './fixtures.mjs'
import { CURSOR_SCRIPT, Director } from './director.mjs'
import { encodePoster, encodeWebp, frameAt, startCapture } from './capture.mjs'
import { FLOWS } from './flows.mjs'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const ROOT = path.resolve(HERE, '../..')
const DIST = path.join(ROOT, 'web/dist/index.html')
const MOCK = path.join(ROOT, 'web/test/e2e/mock-server/server.mjs')
const OUT = path.join(ROOT, 'docs/assets/demos')

/** The clip size: 1280×800 CSS pixels, recorded at 1×. */
const VIEWPORT = { width: 1280, height: 800 }
/** Bytes per clip (UX-AUDIT: about 1.5 MB). */
const BUDGET = 1.5 * 1024 * 1024

function args(argv) {
  const out = { only: undefined, check: false, out: OUT, speed: 1 }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--check') out.check = true
    else if (a === '--only') out.only = argv[++i].split(',')
    else if (a === '--out') out.out = path.resolve(argv[++i])
    else if (a === '--speed') out.speed = Number(argv[++i])
    else if (a === '--help' || a === '-h') {
      console.log('usage: node record.mjs [--only flow,…] [--out dir] [--check] [--speed n]')
      console.log(`flows: ${FLOWS.map((f) => f.name).join(', ')}`)
      process.exit(0)
    } else throw new Error(`record-demos: unknown argument ${a}`)
  }
  return out
}

function freePort() {
  return new Promise((resolve, reject) => {
    const s = net.createServer()
    s.unref()
    s.on('error', reject)
    s.listen(0, () => { const { port } = s.address(); s.close(() => resolve(port)) })
  })
}

async function startMockServer() {
  const port = await freePort()
  const child = spawn(process.execPath, [MOCK], { env: { ...process.env, KARDINAL_E2E_PORT: String(port) }, stdio: ['ignore', 'pipe', 'inherit'] })
  await new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error('record-demos: the mock server did not start in 10 s')), 10_000)
    child.stdout.on('data', (b) => { if (String(b).includes('running')) { clearTimeout(t); resolve() } })
    child.on('exit', (code) => reject(new Error(`record-demos: the mock server exited (${code})`)))
  })
  return { base: `http://localhost:${port}`, stop: () => child.kill() }
}

async function recordFlow(browser, base, flow, theme, opts) {
  const context = await browser.newContext({
    viewport: VIEWPORT, deviceScaleFactor: 1, colorScheme: theme, reducedMotion: 'no-preference', locale: 'en-US', timezoneId: 'UTC',
  })
  const state = createFixtures(flow.fixtures)
  await context.route('**/api/v1/ui/**', apiHandler(state))
  await context.addInitScript((t) => { try { localStorage.setItem('kardinal-theme', t) } catch { /* private mode */ } }, theme)
  await context.addInitScript(CURSOR_SCRIPT)
  const page = await context.newPage()
  await page.clock.install({ time: NOW })
  const errors = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto(`${base}/ui/${flow.start}`)
  await page.waitForLoadState('networkidle')
  await page.waitForTimeout(400)

  const stop = await startCapture(page, VIEWPORT)
  const d = new Director(page, { speed: opts.speed })
  try {
    await flow.run(d, page)
  } finally {
    var frames = await stop()
    await context.close()
  }
  if (errors.length) throw new Error(`record-demos: ${flow.name} (${theme}): page errors: ${errors.join('; ')}`)
  if (frames.length < 2) throw new Error(`record-demos: ${flow.name} (${theme}): only ${frames.length} frames`)

  const clip = await encodeWebp(frames, { budget: BUDGET, width: VIEWPORT.width })
  const poster = await encodePoster(frameAt(frames, d.posterAt ? d.posterAt / 1000 : Infinity).data, { width: VIEWPORT.width })
  const file = `${flow.name}${theme === 'dark' ? '-dark' : ''}`
  fs.mkdirSync(opts.out, { recursive: true })
  fs.writeFileSync(path.join(opts.out, `${file}.webp`), clip.webp)
  fs.writeFileSync(path.join(opts.out, `${file}.png`), poster)
  return { file, bytes: clip.webp.length, posterBytes: poster.length, ...clip }
}

async function main() {
  const opts = args(process.argv.slice(2))
  if (opts.check) opts.out = fs.mkdtempSync(path.join(os.tmpdir(), 'kardinal-demos-'))
  if (!fs.existsSync(DIST)) {
    console.error('record-demos: web/dist is missing; build the UI first (cd web && npm ci && npm run build)')
    process.exit(2)
  }
  const flows = FLOWS.filter((f) => !opts.only || opts.only.includes(f.name))
  if (opts.only && flows.length !== opts.only.length) {
    console.error(`record-demos: unknown flow in --only; flows: ${FLOWS.map((f) => f.name).join(', ')}`)
    process.exit(2)
  }

  const server = await startMockServer()
  const browser = await chromium.launch()
  const results = []
  let failed = false
  try {
    for (const flow of flows) {
      for (const theme of flow.themes) {
        try {
          const r = await recordFlow(browser, server.base, flow, theme, opts)
          const over = r.bytes > BUDGET
          failed ||= over
          results.push(r)
          console.log(`${over ? 'OVER' : 'ok  '} ${r.file.padEnd(16)} ${(r.bytes / 1024).toFixed(0).padStart(5)} KB  ${String(r.frames).padStart(4)} frames  ${(r.durationMs / 1000).toFixed(1).padStart(5)} s  ${r.fps} fps q${r.quality}  poster ${(r.posterBytes / 1024).toFixed(0)} KB`)
        } catch (e) {
          failed = true
          console.error(`FAIL ${flow.name} (${theme}): ${e.message}`)
        }
      }
    }
  } finally {
    await browser.close()
    server.stop()
  }
  console.log(`record-demos: ${results.length} clips in ${opts.out}${failed ? ' — some failed or are over the budget' : ''}`)
  process.exit(failed ? 1 : 0)
}

await main()
