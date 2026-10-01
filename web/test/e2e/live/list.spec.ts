// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-LIST-01: the pipeline sidebar (PAUSED, Blocked, Degraded) and the fleet
// health bar with its filters. Go test: TestUI_BrowserPipelineList
// (test/e2e/live/ui_browser_test.go).
//
// The Go test made three Pipelines in KARDINAL_UI_NAMESPACE: podinfo, whose
// Bundle is Verified in test and held before prod by a gate; the paused
// KARDINAL_UI_PAUSED, with no Bundle; and KARDINAL_UI_BROKEN, whose Bundle
// failed in its only environment. Other tests' pipelines share the sidebar,
// so the spec filters it to the namespace, and checks the fleet bar's counts
// against the pipeline list the page last read.

import { test, expect, type Page } from '@playwright/test'
import { need, filterSidebar, sidebarRows, sidebarRow, PIPELINE } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const paused = need('KARDINAL_UI_PAUSED')
const broken = need('KARDINAL_UI_BROKEN')

/** The fields of GET /api/v1/ui/pipelines the fleet bar counts. */
interface ListedPipeline {
  paused?: boolean
  blockerCount?: number
  failedStepCount?: number
  environmentStates?: Record<string, string>
}

/** lastPipelineList is the pipeline list the page read last. */
function lastPipelineList(page: Page): () => ListedPipeline[] | undefined {
  let last: ListedPipeline[] | undefined
  page.on('response', async r => {
    if (new URL(r.url()).pathname !== '/api/v1/ui/pipelines' || !r.ok()) return
    try { last = await r.json() as ListedPipeline[] } catch { /* the page moved on */ }
  })
  return () => last
}

const inFlight = new Set(['Promoting', 'WaitingForMerge', 'HealthChecking', 'RollingBack'])

/** pipelines is "1 pipeline" or "N pipelines", as the fleet bar badges say. */
function pipelines(n: number, word = 'pipeline'): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

test('the sidebar shows each pipeline’s state, and the filters narrow it', async ({ page }) => {
  const listed = lastPipelineList(page)
  await page.goto(`${base}/ui/`)
  await filterSidebar(page, ns)
  await expect(sidebarRows(page)).toHaveCount(3)

  const held = sidebarRow(page, PIPELINE)
  await expect(held.locator('.health-chip')).toHaveText('Blocked')
  await expect(held.locator('.health-chip')).toHaveAttribute('data-health-state', 'Reconciling')
  await expect(held).toContainText('2 envs')
  await expect(held).toContainText('1 verified')
  await expect(held).not.toContainText('PAUSED')

  const pausedRow = sidebarRow(page, paused)
  await expect(pausedRow.getByTitle('Pipeline is paused — no new promotions will start')).toHaveText('PAUSED')
  await expect(pausedRow.locator('.health-chip')).toHaveText('Paused')
  await expect(pausedRow.locator('.health-chip')).toHaveAttribute('data-health-state', 'Paused')
  await expect(pausedRow).toContainText('1 env')

  const brokenRow = sidebarRow(page, broken)
  await expect(brokenRow.locator('.health-chip')).toHaveText('Degraded')
  await expect(brokenRow.locator('.health-chip')).toHaveAttribute('data-health-state', 'Degraded')
  await expect(brokenRow).toContainText('1 failed')
  await expect(brokenRow).not.toContainText('PAUSED')

  // The name filter matches namespace/name too.
  await filterSidebar(page, `${ns}/${PIPELINE}-`)
  await expect(sidebarRows(page)).toHaveCount(2)
  await expect(sidebarRow(page, PIPELINE)).toHaveCount(0)
  await filterSidebar(page, `${ns}-none`)
  await expect(sidebarRows(page)).toHaveCount(0)
  await expect(page.getByText(`No pipelines match “${ns}-none”`)).toBeVisible()
  await filterSidebar(page, ns)
  await expect(sidebarRows(page)).toHaveCount(3)

  // The fleet bar counts every pipeline the API lists.
  const fleet = page.getByLabel('Fleet health summary')
  await expect(async () => {
    const list = listed()
    expect(list, 'the page read the pipeline list').toBeDefined()
    const count = (f: (p: ListedPipeline) => boolean) => list!.filter(f).length
    const blocked = count(p => (p.blockerCount ?? 0) > 0)
    const ciRed = count(p => (p.failedStepCount ?? 0) > 0)
    const healthy = count(p => !p.paused && (p.blockerCount ?? 0) === 0 && (p.failedStepCount ?? 0) === 0)
    const promoting = count(p => Object.values(p.environmentStates ?? {}).some(s => inFlight.has(s)))
    await expect(fleet.getByRole('button', { name: 'Show all pipelines' })).toHaveText(`Pipelines${list!.length}`, { timeout: 500 })
    await expect(fleet.getByRole('button', { name: pipelines(healthy, 'healthy pipeline'), exact: true })).toBeVisible({ timeout: 500 })
    await expect(fleet.getByRole('button', { name: pipelines(blocked, 'blocked pipeline'), exact: true })).toBeVisible({ timeout: 500 })
    await expect(fleet.getByRole('button', { name: `${pipelines(ciRed)} with CI failures`, exact: true })).toBeVisible({ timeout: 500 })
    await expect(fleet.getByRole('button', { name: `${pipelines(promoting)} currently promoting`, exact: true })).toBeVisible({ timeout: 500 })
  }).toPass({ timeout: 30_000 })

  // Each badge filters the list, and a second click clears the filter.
  const all = fleet.getByRole('button', { name: 'Show all pipelines' })
  const blockedBadge = fleet.getByRole('button', { name: /^\d+ blocked pipelines?$/ })
  const ciRedBadge = fleet.getByRole('button', { name: /^\d+ pipelines? with CI failures$/ })
  const healthyBadge = fleet.getByRole('button', { name: /^\d+ healthy pipelines?$/ })
  await expect(all).toHaveAttribute('aria-pressed', 'true')

  await blockedBadge.click()
  await expect(blockedBadge).toHaveAttribute('aria-pressed', 'true')
  await expect(all).toHaveAttribute('aria-pressed', 'false')
  await expect(sidebarRows(page)).toHaveCount(1)
  await expect(sidebarRow(page, PIPELINE)).toHaveCount(1)

  await ciRedBadge.click()
  await expect(ciRedBadge).toHaveAttribute('aria-pressed', 'true')
  await expect(blockedBadge).toHaveAttribute('aria-pressed', 'false')
  await expect(sidebarRows(page)).toHaveCount(1)
  await expect(sidebarRow(page, broken)).toHaveCount(1)

  // A paused, a blocked and a failed pipeline are not healthy.
  await healthyBadge.click()
  await expect(healthyBadge).toHaveAttribute('aria-pressed', 'true')
  await expect(sidebarRows(page)).toHaveCount(0)
  await expect(page.getByText(`No pipelines match “${ns}”`)).toBeVisible()

  await healthyBadge.click()
  await expect(all).toHaveAttribute('aria-pressed', 'true')
  await expect(sidebarRows(page)).toHaveCount(3)
  await blockedBadge.click()
  await all.click()
  await expect(all).toHaveAttribute('aria-pressed', 'true')
  await expect(sidebarRows(page)).toHaveCount(3)
})

test('selecting a row opens its pipeline, with its paused state', async ({ page }) => {
  await page.goto(`${base}/ui/`)
  await filterSidebar(page, ns)
  const pausedRow = sidebarRow(page, paused)
  await pausedRow.click()
  await expect(pausedRow).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByRole('heading', { level: 1, name: paused })).toBeVisible()
  await expect(page.getByText('⏸ PAUSED — no new promotions')).toBeVisible()
  expect(new URLSearchParams(new URL(page.url()).hash.slice(1)).get('pipeline')).toBe(paused)
  expect(new URLSearchParams(new URL(page.url()).hash.slice(1)).get('ns')).toBe(ns)

  const held = sidebarRow(page, PIPELINE)
  await held.click()
  await expect(held).toHaveAttribute('aria-pressed', 'true')
  await expect(pausedRow).toHaveAttribute('aria-pressed', 'false')
  await expect(page.getByRole('heading', { level: 1, name: PIPELINE })).toBeVisible()
  await expect(page.getByText('⏸ PAUSED — no new promotions')).toHaveCount(0)
})
