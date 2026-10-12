import { spawn } from 'node:child_process'
import { chromium } from 'playwright'
import { apiHandler, createFixtures, NOW } from './fixtures.mjs'
const [hash, out] = process.argv.slice(2)
const port = 3341
const child = spawn(process.execPath, ['../../web/test/e2e/mock-server/server.mjs'], { env: { ...process.env, KARDINAL_E2E_PORT: String(port) }, stdio: 'ignore' })
await new Promise(r => setTimeout(r, 1200))
const b = await chromium.launch(); const c = await b.newContext({ viewport: { width: 1280, height: 800 } })
const reqs = []
c.on('request', r => { if (r.url().includes('/api/')) reqs.push(r.method() + ' ' + new URL(r.url()).pathname) })
await c.route('**/api/v1/ui/**', apiHandler(createFixtures({ big: process.argv[4] === 'big' })))
const p = await c.newPage(); await p.clock.install({ time: NOW })
await p.goto(`http://localhost:${port}/ui/${hash}`); await p.waitForTimeout(1800)
await p.screenshot({ path: out }); console.log(await p.locator('main').ariaSnapshot()); console.log([...new Set(reqs)].join('\n'))
await b.close(); child.kill()
