// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-WAVE-01: a wave drawn as a wave (#1580). Go test: TestUI_BrowserWaves
// (test/e2e/live/ui_browser_test.go).
//
// Pipeline podinfo in KARDINAL_UI_NAMESPACE is test, then w1…w6 in wave 1
// after it. Bundle KARDINAL_UI_BUNDLE is Verified in all seven.

import { test, expect } from '@playwright/test'
import { need, openPipeline, dagNode, filterSidebar, PIPELINE } from './live'

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

test('the lane counts the wave, the DAG keeps spec order, the metrics count the final wave', async ({ page }) => {
  await openPipeline(page, base, ns)
  const lane = page.getByRole('group', { name: 'Pipeline stages' })
  const wave = lane.getByRole('group', { name: '6 environments: w1 to w6' })
  await expect(wave).toContainText('6 verified')
  await expect(lane.getByRole('button', { name: /^Select / })).toHaveCount(1)
  await wave.getByRole('button', { name: 'Show environments' }).click()
  for (const env of WAVE) await expect(wave.getByRole('button', { name: `Select ${env}` })).toBeVisible()

  const ys: number[] = []
  for (const env of WAVE) {
    await expect(dagNode(page, env)).toHaveAttribute('aria-label', `${env} — Verified`)
    ys.push((await dagNode(page, env).boundingBox())!.y)
  }
  expect(ys, 'w1 at the top, w6 at the bottom').toEqual([...ys].sort((a, b) => a - b))

  const metrics = page.getByRole('region', { name: 'Release metrics' })
  await expect(metrics).toContainText('Time to all 6 final envs')
  await expect(metrics).toContainText(/Deploys to all 6 final envs\s*1/)
  await expect(metrics).not.toContainText('w6')
})
