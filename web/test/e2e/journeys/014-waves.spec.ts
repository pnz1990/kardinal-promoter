// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 014: a Pipeline with a wave of 149 environments after env-000
// (#1580). The fleet board shows the wave as one plate, the station map as one
// band that expands into a grid in spec order, and the release metrics count
// the final wave, not its last entry.

import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

const WAVE = Array.from({ length: 149 }, (_, i) => `env-${String(i + 1).padStart(3, '0')}`)
const created = Date.now() - 3 * 3_600_000

const pipeline = {
  name: 'fleet',
  namespace: 'waves',
  phase: 'Ready',
  environmentCount: 150,
  activeBundleName: 'fleet-2',
  activeBundleVersion: '2.0.0',
  blockerCount: 0,
  failedStepCount: 0,
  environmentTopology: [{ name: 'env-000' }, ...WAVE.map(name => ({ name, upstreams: ['env-000'] }))],
  topologyResolved: true,
  environmentStates: Object.fromEntries(['env-000', ...WAVE].map(e => [e, 'Verified'])),
  deployed: Object.fromEntries(['env-000', ...WAVE].map(e => [e, { bundle: 'fleet-2', version: '2.0.0' }])),
  // status.summary counts a wave longer than a clause names under the wave.
  summary: '2.0.0 live in env-000 and all 149 wave 1',
}

const verified = (env: string, hours: number) => ({ name: env, phase: 'Verified', healthCheckedAt: new Date(created + hours * 3_600_000).toISOString() })
const bundles = [{
  name: 'fleet-2', namespace: 'waves', phase: 'Verified', type: 'image', pipeline: 'fleet',
  createdAt: new Date(created).toISOString(),
  // env-149 was the first of the wave to finish; env-001 took 2h.
  environments: [verified('env-000', 0.1), ...WAVE.map(e => verified(e, e === 'env-001' ? 2 : e === 'env-149' ? 0.5 : 1))],
}]

const graph = {
  nodes: ['env-000', ...WAVE].map(env => ({ id: `step-${env}`, type: 'PromotionStep', label: env, environment: env, state: 'Verified' })),
  edges: WAVE.map(env => ({ from: 'step-env-000', to: `step-${env}` })),
}

// Every environment in wave 1: only roots, so no entry has upstreams and the
// controller says topologyResolved (#1580 QA). regions has no Bundle (the DAG
// is the static topology); regions-live has one Verified in all three.
const REGIONS = ['eu', 'us', 'ap']
const allWave1 = (name: string, bundle?: string) => ({
  name, namespace: 'waves', phase: 'Ready', environmentCount: 3, blockerCount: 0, failedStepCount: 0,
  environmentTopology: REGIONS.map(env => ({ name: env })),
  topologyResolved: true,
  ...(bundle ? { activeBundleName: bundle, environmentStates: Object.fromEntries(REGIONS.map(e => [e, 'Verified'])) } : {}),
})
const regionBundles = [{
  name: 'regions-live-1', namespace: 'waves', phase: 'Verified', type: 'image', pipeline: 'regions-live',
  createdAt: new Date(created).toISOString(),
  environments: REGIONS.map((e, i) => verified(e, i + 1)),
}]
const regionGraph = {
  nodes: REGIONS.map(env => ({ id: `step-${env}`, type: 'PromotionStep', label: env, environment: env, state: 'Verified' })),
  edges: [],
}

async function serveWave(page: Page) {
  await page.route('**/api/v1/ui/pipelines', async route => {
    const res = await route.fetch()
    await route.fulfill({ response: res, json: [...await res.json(), pipeline, allWave1('regions'), allWave1('regions-live', 'regions-live-1')] })
  })
  await page.route('**/api/v1/ui/pipelines/regions/bundles**', route => route.fulfill({ json: [] }))
  await page.route('**/api/v1/ui/pipelines/regions-live/bundles**', route => route.fulfill({ json: regionBundles }))
  await page.route('**/api/v1/ui/bundles/regions-live-1/graph**', route => route.fulfill({ json: regionGraph }))
  await page.route('**/api/v1/ui/pipelines/fleet/bundles**', route => route.fulfill({ json: bundles }))
  await page.route('**/api/v1/ui/bundles/fleet-2/graph**', route => route.fulfill({ json: graph }))
}

test.describe('Journey 014 — Waves', () => {
  test('the fleet board shows a wave as one plate', async ({ page }) => {
    await serveWave(page)
    await page.goto('/')
    const board = page.getByRole('region', { name: 'Fleet' })
    const plate = board.getByRole('button', { name: /^fleet env-001 to env-149, 149 environments:/ })
    await expect(plate).toContainText('149 environments')
    await expect(plate).toContainText('2.0.0')
    await expect(board.getByRole('button', { name: /^fleet / })).toHaveCount(2)

    // The plate passes the WCAG 2.1 AA checks, colour contrast included.
    const axe = await new AxeBuilder({ page }).include('.fleet-station--wave').withTags(['wcag2a', 'wcag2aa']).analyze()
    expect(axe.violations.map(v => `${v.id}: ${v.nodes[0]?.target[0]}`)).toEqual([])
  })

  test('the map groups the wave into a band, keeps spec order, the metrics count the final wave', async ({ page }) => {
    await serveWave(page)
    await page.goto('/')
    await page.locator('aside').getByText('fleet', { exact: true }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'fleet' })).toBeVisible()
    await expect(page.locator('.app-pipeline-head__summary')).toContainText('2.0.0 live in env-000 and all 149 wave 1')

    // The station map draws the wave of 149 as one band (U2).
    const map = page.locator('.station-map')
    const band = map.getByRole('button', { name: /^WAVE 1 · 149: 149 live$/ })
    await expect(band).toHaveAttribute('aria-expanded', 'false')
    await expect(map.locator('[data-station^="env-"]').and(map.locator('.cell'))).toHaveCount(0)
    await band.click()
    await expect(band).toHaveAttribute('aria-expanded', 'true')
    const cells = map.locator('.cell[data-station]')
    await expect(cells).toHaveCount(149)
    // A cell opens its environment's stage drawer.
    await map.locator('[data-station="env-005"]').click()
    await expect(page.getByLabel('Close')).toBeVisible()
    await page.getByLabel('Close').click()

    // The grid reads in spec order from env-001 (all are Verified).
    // Both cells are measured in one frame: the map may still be refitting after the drawer closed.
    const before = (a: string, b: string) => page.evaluate(([a, b]) => {
      const [p, q] = [a, b].map(env => document.querySelector(`.station-map [data-station="${env}"]`)!.getBoundingClientRect())
      return p.y < q.y - 1 || (Math.abs(p.y - q.y) <= 1 && p.x < q.x)
    }, [a, b])
    await expect.poll(() => before('env-001', 'env-002')).toBe(true)
    await expect.poll(() => before('env-002', 'env-149')).toBe(true)

    // The band and its grid pass the WCAG 2.1 AA checks.
    const axe = await new AxeBuilder({ page }).include('.station-map').withTags(['wcag2a', 'wcag2aa']).analyze()
    expect(axe.violations.map(v => `${v.id}: ${v.nodes[0]?.target[0]}`)).toEqual([])

    // The release metrics are the Metrics tab.
    await page.getByRole('tab', { name: 'Metrics' }).click()
    const metrics = page.getByRole('region', { name: 'Release metrics' })
    await expect(metrics).toContainText('Time to all 149 final envs')
    // The release ended when the slowest of the wave (env-001, 2h) verified.
    await expect(metrics).toContainText('2h')
    await expect(metrics).not.toContainText('env-149')
  })

  test('an all-wave-1 Pipeline is drawn as parallel roots, not a chain', async ({ page }) => {
    await serveWave(page)
    await page.goto('/')
    // The fleet board stacks the three in one stop.
    const board = page.getByRole('region', { name: 'Fleet' })
    const line = board.locator('li.fleet-line').filter({ has: page.getByRole('button', { name: /^regions$/ }) })
    await expect(line.locator('.fleet-stop')).toHaveCount(1)
    await expect(line.locator('.fleet-station')).toHaveCount(3)

    // With no Bundle the map is the topology: three roots in one column, no edges.
    await page.locator('aside').getByText('regions', { exact: true }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'regions' })).toBeVisible()
    const xs = await Promise.all(REGIONS.map(async env => Math.round((await page.locator(`.station-map [data-station="${env}"]`).boundingBox())!.x)))
    expect(new Set(xs).size, 'one column').toBe(1)
    await expect(page.locator('.station-map__edge')).toHaveCount(0)

    // With a Bundle the metrics count all three as final environments.
    await page.locator('aside').getByText('regions-live', { exact: true }).click()
    await page.getByRole('tab', { name: 'Metrics' }).click()
    const metrics = page.getByRole('region', { name: 'Release metrics' })
    await expect(metrics).toContainText('Time to all 3 final envs')
    // The last of them (ap) was Verified 3h after creation.
    await expect(metrics).toContainText('3h')
  })
})

