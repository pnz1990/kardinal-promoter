// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-WAVE-01: a wave drawn as a wave (#1580). Go test: TestUI_BrowserWaves
// (test/e2e/live/ui_browser_test.go).
//
// Pipeline podinfo in KARDINAL_UI_NAMESPACE is test, then w1…w6 in wave 1
// after it. Bundle KARDINAL_UI_BUNDLE is Verified in all seven.

import { test, expect } from '@playwright/test'
import { need, openPipeline, station, filterSidebar, PIPELINE, stageDrawer, drawerHeading } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
need('KARDINAL_UI_BUNDLE')
const WAVE = ['w1', 'w2', 'w3', 'w4', 'w5', 'w6']

test('the fleet board shows the wave as one plate', async ({ page }) => {
  await page.goto(`${base}/ui/`)
  await filterSidebar(page, ns)
  const board = page.getByRole('region', { name: 'Fleet' })
  const plateName = new RegExp(`^${PIPELINE} w1 to w6, 6 environments:`)
  const plate = board.getByRole('button', { name: plateName })
  await expect(plate).toContainText('6 environments')
  await expect(plate).toContainText('w1 … w6')
  await expect(plate).toHaveAttribute('data-state', 'settled')
  // Its line has test's station and the wave plate: no station per wave environment.
  const line = board.locator('li.fleet-line').filter({ has: page.getByRole('button', { name: plateName }) })
  await expect(line.locator('.fleet-station')).toHaveCount(2)
})

test('the map draws the wave as a band, its grid keeps spec order, the metrics count the final wave', async ({ page }) => {
  await openPipeline(page, base, ns)
  const map = page.locator('.station-map')
  // The station map (U2): six parallel environments are one band.
  const band = map.getByRole('button', { name: /^WAVE 1 · 6: 6 live$/ })
  await expect(band).toHaveAttribute('aria-expanded', 'false')
  await expect(map.locator('[data-station]')).toHaveCount(1) // test; the wave is the band
  await band.click()
  await expect(band).toHaveAttribute('aria-expanded', 'true')
  for (const env of WAVE) await expect(station(page, env).or(map.locator(`.cell[data-station="${env}"]`))).toBeVisible()
  // A real click on a cell opens its environment (no force).
  await map.locator('.cell[data-station="w3"]').click()
  await expect(drawerHeading(page)).toHaveText('w3')
  await stageDrawer(page).getByRole('button', { name: 'Close w3' }).click()

  // The grid reads in spec order: w1 first, w6 last.
  const order: Array<{ x: number; y: number }> = []
  for (const env of WAVE) {
    const cell = map.locator(`.cell[data-station="${env}"]`)
    await expect(cell).toHaveAttribute('aria-label', new RegExp(`^${env} — Live, `))
    order.push((await cell.boundingBox())!)
  }
  for (let i = 1; i < order.length; i++) {
    const [p, q] = [order[i - 1], order[i]]
    expect(p.y < q.y - 1 || (Math.abs(p.y - q.y) <= 1 && p.x < q.x), `${WAVE[i - 1]} before ${WAVE[i]}`).toBe(true)
  }

  // The release metrics are on the Metrics tab (U1).
  await page.getByRole('tab', { name: 'Metrics' }).click()
  const metrics = page.getByRole('region', { name: 'Release metrics' })
  await expect(metrics).toContainText('Time to all 6 final envs')
  await expect(metrics).toContainText(/Deploys to all 6 final envs\s*1/)
  await expect(metrics).not.toContainText('w6')
})
