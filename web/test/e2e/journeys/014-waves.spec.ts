// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 014: a Pipeline with a wave of 149 environments after env-000
// (#1580). The fleet board shows the wave as one plate, the stage lane as one
// wave card that expands, the DAG lists the wave top to bottom in spec order,
// and the release metrics count the final wave, not its last entry.

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
  environmentStates: Object.fromEntries(['env-000', ...WAVE].map(e => [e, 'Verified'])),
  deployed: Object.fromEntries(['env-000', ...WAVE].map(e => [e, { bundle: 'fleet-2', version: '2.0.0' }])),
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

async function serveWave(page: Page) {
  await page.route('**/api/v1/ui/pipelines', async route => {
    const res = await route.fetch()
    await route.fulfill({ response: res, json: [...await res.json(), pipeline] })
  })
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
  })

  test('the lane groups the wave, the DAG keeps spec order, the metrics count the final wave', async ({ page }) => {
    await serveWave(page)
    await page.goto('/')
    await page.locator('aside').getByText('fleet', { exact: true }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'fleet' })).toBeVisible()

    const lane = page.getByRole('group', { name: 'Pipeline stages' })
    const wave = lane.getByRole('group', { name: '149 environments: env-001 to env-149' })
    await expect(wave).toContainText('149 verified')
    await expect(lane.getByRole('button', { name: /^Select env-/ })).toHaveCount(1)
    await wave.getByRole('button', { name: 'Show environments' }).click()
    await expect(wave.getByRole('button', { name: /^Select env-/ })).toHaveCount(149)

    // DAG nodes of the wave run top to bottom from env-001.
    const y = async (env: string) => (await page.locator(`[data-node-id="step-${env}"]`).boundingBox())!.y
    expect(await y('env-001')).toBeLessThan(await y('env-002'))
    expect(await y('env-002')).toBeLessThan(await y('env-149'))

    const metrics = page.getByRole('region', { name: 'Release metrics' })
    await expect(metrics).toContainText('Time to all 149 final envs')
    // The release ended when the slowest of the wave (env-001, 2h) verified.
    await expect(metrics).toContainText('2h')
    await expect(metrics).not.toContainText('env-149')

    // The wave card and its expanded list pass the WCAG 2.1 AA checks.
    const axe = await new AxeBuilder({ page }).include('[aria-label="Pipeline stages"]').withTags(['wcag2a', 'wcag2aa']).analyze()
    expect(axe.violations.map(v => `${v.id}: ${v.nodes[0]?.target[0]}`)).toEqual([])
  })
})
