// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-LIST-01: the pipeline sidebar (Paused, Blocked, Degraded) and the fleet
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
import { need, filterSidebar, sidebarRows, sidebarRow, PIPELINE, route } from './live'

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
  // Its one state (pipelineState): held, named from status.environments.
  await expect(held.locator('[data-category]')).toHaveAttribute('data-category', 'blocked')
  // Compact in the row (Blocked · N gates); the whole state is its title.
  await expect(held.locator('[data-category]')).toContainText(/^.?\s*Blocked · \d+ gates?$/)
  await expect(held.locator('[data-category]')).toHaveAttribute('title', /^(Held by gates? .+ in |Blocked by)/)
  await expect(held).toContainText('2 envs')
  await expect(held).toContainText('1 live')
  await expect(held).not.toContainText('Paused')

  const pausedRow = sidebarRow(page, paused)
  // Paused once: the state chip, titled with what it means (no second PAUSED badge).
  await expect(pausedRow.getByTitle('Pipeline is paused — no new promotions will start')).toHaveText(/Paused$/)
  await expect(pausedRow.locator('[data-category]')).toHaveCount(1)
  await expect(pausedRow.locator('[data-category]')).toHaveAttribute('data-category', 'paused')
  await expect(pausedRow).toContainText('1 env')

  const brokenRow = sidebarRow(page, broken)
  await expect(brokenRow.locator('[data-category]')).toContainText('A version failed')
  await expect(brokenRow.locator('[data-category]')).toHaveAttribute('data-category', 'failed')
  await expect(brokenRow).toContainText('1 failed')
  await expect(brokenRow).not.toContainText('Paused')

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
    await expect(fleet.getByRole('button', { name: `${pipelines(ciRed)} with a failed promotion`, exact: true })).toBeVisible({ timeout: 500 })
    await expect(fleet.getByRole('button', { name: `${pipelines(promoting)} currently promoting`, exact: true })).toBeVisible({ timeout: 500 })
  }).toPass({ timeout: 30_000 })

  // Each badge filters the list, and a second click clears the filter.
  const all = fleet.getByRole('button', { name: 'Show all pipelines' })
  const blockedBadge = fleet.getByRole('button', { name: /^\d+ blocked pipelines?$/ })
  const ciRedBadge = fleet.getByRole('button', { name: /^\d+ pipelines? with a failed promotion$/ })
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
  // The header says Paused and the map carries the paused band (U2).
  await expect(page.getByText('⏸ PAUSED — no new promotions')).toBeVisible()
  expect(route(page).pipeline).toBe(paused)
  expect(route(page).ns).toBe(ns)

  const held = sidebarRow(page, PIPELINE)
  await held.click()
  await expect(held).toHaveAttribute('aria-pressed', 'true')
  await expect(pausedRow).toHaveAttribute('aria-pressed', 'false')
  await expect(page.getByRole('heading', { level: 1, name: PIPELINE })).toBeVisible()
  await expect(page.getByRole('status').filter({ hasText: 'Paused · no new promotions' })).toHaveCount(0)
})

// UI-FLEET-01: with no pipeline selected, the fleet board draws each pipeline
// as stations: podinfo runs its Bundle in test and is held by a gate before
// prod, the broken pipeline failed in its only environment, and the paused
// one has nothing deployed. A station opens its pipeline; the logo returns.
test('the fleet board shows what each environment runs and where releases are', async ({ page }) => {
  await page.goto(`${base}/ui/`)
  const board = page.getByRole('region', { name: 'Fleet' })
  const line = (name: string) => board.getByRole('listitem').filter({ has: page.locator('.fleet-line__ns', { hasText: new RegExp(`^${ns}$`) }) })
    .filter({ has: page.getByRole('button', { name, exact: true }) })

  const held = line(PIPELINE)
  await expect(held).toHaveCount(1)
  const testStation = held.getByRole('button', { name: new RegExp(`^${PIPELINE} test:`) })
  await expect(testStation).toHaveAttribute('data-state', 'settled')
  await expect(testStation.locator('.fleet-station__version')).not.toHaveText('—')
  await expect(testStation).toContainText(/verified (just now|\d+[mhd] ago)/)
  await expect(held.getByRole('button', { name: new RegExp(`^${PIPELINE} prod:`) })).toHaveAttribute('data-state', 'held')
  await expect(held).toHaveAttribute('data-live', 'held')
  await expect(held.locator('[data-category="blocked"]')).toBeVisible()

  const failed = line(broken)
  await expect(failed.getByRole('button', { name: new RegExp(`^${broken} \\S+:`) })).toHaveAttribute('data-state', 'failed')
  await expect(failed).toHaveAttribute('data-live', 'failed')

  const idle = line(paused)
  await expect(idle.locator('[data-category="paused"]')).toContainText('Paused')
  for (const station of await idle.locator('.fleet-station').all()) {
    await expect(station).toHaveAttribute('data-state', 'empty')
  }

  await testStation.click()
  await expect(page.getByRole('heading', { level: 1, name: PIPELINE })).toBeVisible()
  expect(route(page).ns).toBe(ns)
  await page.getByRole('button', { name: 'Show the fleet' }).click()
  await expect(board).toBeVisible()
})
