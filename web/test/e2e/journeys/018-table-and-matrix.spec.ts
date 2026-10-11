// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 018: the Map tab's other views of the same pipeline (DESIGN-SYSTEM §3.5).
// The Table lists every environment (the map's linear equivalent, sortable); the
// Matrix shows every version in every environment, collapses fleets like the map,
// follows a version on click and compares on Shift-click, and keeps 200 versions ×
// 300 environments fast by rendering only the rows in view. A narrow screen opens
// on the Table.

import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { serveScale } from './scaleFixtures'

async function open(page: Page, pipeline: string, ns: string, query = '') {
  await page.goto(`/ui/#/p/${ns}/${pipeline}${query}`)
  await expect(page.getByRole('heading', { level: 1, name: pipeline })).toBeVisible()
}
const views = (page: Page) => page.getByRole('radiogroup', { name: 'Show the pipeline as' })

test.describe('Journey 018 — Table and Matrix', () => {
  test('the Table lists each environment, sorts by state, opens a drawer, and passes axe', async ({ page }) => {
    await open(page, 'kardinal-test-app', 'default')
    await views(page).getByRole('radio', { name: 'Table' }).click()
    await expect(page).toHaveURL(/as=table/)
    const table = page.getByRole('region', { name: 'Environments' })
    await expect(table.getByRole('row')).toHaveCount(4)
    const prod = table.getByRole('row', { name: /^prod/ })
    await expect(prod).toContainText('Held by gate no-weekend-deploys')
    await expect(prod).toContainText('→ sha-abc1234')
    await expect(prod.getByRole('link', { name: 'PR #42' })).toBeVisible()
    await table.getByRole('button', { name: 'State' }).click()
    await expect(table.getByRole('columnheader', { name: /State/ })).toHaveAttribute('aria-sort', 'ascending')
    await expect(table.getByRole('row').nth(1)).toHaveAttribute('data-station', 'prod')
    await prod.getByRole('button', { name: 'prod' }).click()
    await expect(page.getByTestId('stage-drawer')).toBeVisible()
    const axe = await new AxeBuilder({ page }).include('.stage-table').withTags(['wcag2a', 'wcag2aa']).analyze()
    expect(axe.violations.map(v => `${v.id}: ${v.nodes[0]?.target[0]}`)).toEqual([])
  })

  test('the Matrix shows every version by environment; click follows, Shift-click compares', async ({ page }) => {
    await serveScale(page)
    await open(page, 'checkout-30', 'scale', '?as=matrix')
    const matrix = page.getByRole('region', { name: 'Versions by environment' })
    const current = matrix.locator('tr[data-bundle="checkout-30-7"]')
    await expect(current).toHaveAttribute('data-followed', 'true')
    await expect(current.locator('td[title^="edge · 12"]')).toHaveAttribute('data-tone', 'failed')
    await expect(current.locator('td[title^="audit:"]')).toHaveAttribute('title', 'audit: Not reached')
    // The fleet expands into its 12 targets.
    await matrix.getByRole('button', { name: /^edge · 12/ }).click()
    await expect(current.locator('td[title^="edge-06:"]')).toHaveAttribute('title', 'edge-06: Failed')
    // Shift-click the older version: the comparison opens.
    await matrix.locator('tr[data-bundle="checkout-30-6"]').getByRole('button').first().click({ modifiers: ['Shift'] })
    await expect(page).toHaveURL(/compare=checkout-30-6/)
    await expect(page.getByRole('dialog', { name: 'Version comparison' })).toBeVisible()
    await page.keyboard.press('Escape')
    // A click follows the older version.
    await matrix.locator('tr[data-bundle="checkout-30-6"]').getByRole('button').first().click()
    await expect(matrix.locator('tr[data-bundle="checkout-30-6"]')).toHaveAttribute('data-followed', 'true')
    const axe = await new AxeBuilder({ page }).include('.version-matrix').withTags(['wcag2a', 'wcag2aa']).analyze()
    expect(axe.violations.map(v => `${v.id}: ${v.nodes[0]?.target[0]}`)).toEqual([])
  })

  test('200 versions × 300 environments: only the rows in view are drawn, and the oldest is a scroll away', async ({ page }) => {
    await serveScale(page)
    await page.setViewportSize({ width: 1440, height: 900 })
    await open(page, 'large-300', 'scale', '?as=matrix')
    const matrix = page.getByRole('region', { name: 'Versions by environment' })
    await expect(matrix.locator('tr[data-bundle="large-300-7"]')).toBeVisible()
    expect(await matrix.locator('tr[data-bundle]').count()).toBeLessThan(60)
    const started = Date.now()
    await matrix.evaluate(el => { el.scrollTop = el.scrollHeight })
    await expect(matrix.locator('tr[data-bundle="large-300-old-198"]')).toBeVisible()
    expect(Date.now() - started).toBeLessThan(2000)
    expect(await matrix.locator('tr[data-bundle]').count()).toBeLessThan(60)
  })

  test('a narrow screen opens on the Table; the Map is one choice away', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await open(page, 'kardinal-test-app', 'default')
    await expect(page.getByRole('region', { name: 'Environments' })).toBeVisible()
    await views(page).getByRole('radio', { name: 'Map' }).click()
    await expect(page.locator('.station-map__svg')).toBeVisible()
  })

  test('the Table and the Matrix load their code only when first shown', async ({ page }) => {
    const chunks: string[] = []
    page.on('request', r => { const m = r.url().match(/\/assets\/(StageTable|VersionMatrix)-[^/]+\.js$/); if (m) chunks.push(m[1]) })
    await open(page, 'kardinal-test-app', 'default')
    await expect(page.locator('.station-map__svg')).toBeVisible()
    expect(chunks).toEqual([])
    await views(page).getByRole('radio', { name: 'Matrix' }).click()
    await expect(page.getByRole('region', { name: 'Versions by environment' })).toBeVisible()
    expect(chunks).toEqual(['VersionMatrix'])
  })
})

