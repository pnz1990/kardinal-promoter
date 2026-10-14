// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 024 (#1859): the fleet board, the station map, the stage drawer, the
// sidebar's virtual list and the ship dialog are chunks of their own, loaded
// when a route needs them. No route shifts while a chunk arrives: the skeleton
// holds the view's place, and the drawer's placeholder takes the drawer's
// column before the drawer's code or the pipeline arrives (an /env/ link
// shifted 0.26 before). What remains, under MAX_SHIFT, is the pipeline's
// conditions banner arriving with its data. Each route passes axe.

import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

/** The cumulative layout shift a route may have: the pipeline's conditions
 *  banner arriving with its data (about 0.036), no view loading. */
const MAX_SHIFT = 0.05

/** Starts recording layout shifts before the page's own scripts run. */
async function recordShifts(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as { __shift: number }
    w.__shift = 0
    new PerformanceObserver(list => {
      for (const e of list.getEntries() as Array<PerformanceEntry & { value: number; hadRecentInput: boolean }>) {
        if (!e.hadRecentInput) w.__shift += e.value
      }
    }).observe({ type: 'layout-shift', buffered: true })
  })
}

async function shift(page: Page): Promise<number> {
  // Let late chunks and the first poll land.
  await page.waitForTimeout(1500)
  return page.evaluate(() => (window as unknown as { __shift: number }).__shift)
}

async function axe(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target).join(' ')}`)).toEqual([])
}

test.describe('Journey 024 — split views load without a layout shift', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await recordShifts(page)
  })

  test('the first page: the fleet board', async ({ page }) => {
    await page.goto('/ui/')
    await expect(page.locator('.fleet-board')).toBeVisible()
    expect(await shift(page)).toBeLessThan(MAX_SHIFT)
    await axe(page)
  })

  test('a pipeline: the station map', async ({ page }) => {
    await page.goto('/ui/#/p/default/payments-service')
    await expect(page.locator('.station-map')).toBeVisible()
    expect(await shift(page)).toBeLessThan(MAX_SHIFT)
    await axe(page)
  })

  test('an /env/ link: the map and the stage drawer', async ({ page }) => {
    await page.goto('/ui/#/p/default/payments-service/env/prod')
    await expect(page.getByTestId('stage-drawer')).toBeVisible()
    await expect(page.locator('.station-map')).toBeVisible()
    expect(await shift(page)).toBeLessThan(MAX_SHIFT)
    await axe(page)
  })
})
