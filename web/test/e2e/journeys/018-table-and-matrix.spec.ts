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
const views = (page: Page) => page.getByRole('radiogroup', { name: 'View' })

test.describe('Journey 018 — Table and Matrix', () => {
  test('the Table lists each environment, sorts by state, opens a drawer, and passes axe', async ({ page }) => {
    await open(page, 'kardinal-test-app', 'default')
    await views(page).getByRole('radio', { name: 'View: Table' }).click()
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
    await expect(current.locator('td[aria-label^="edge · 12"]')).toHaveAttribute('data-tone', 'failed')
    await expect(current.locator('td[aria-label^="audit:"]')).toHaveAttribute('aria-label', 'audit: Not reached')
    // The fleet expands into its 12 targets.
    await matrix.getByRole('button', { name: /^edge · 12/ }).click()
    await expect(current.locator('td[aria-label^="edge-06:"]')).toHaveAttribute('aria-label', 'edge-06: Failed')
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
    await views(page).getByRole('radio', { name: 'View: Map' }).click()
    await expect(page.locator('.station-map__svg')).toBeVisible()
  })

  test('the Table and the Matrix load their code only when first shown', async ({ page }) => {
    const chunks: string[] = []
    page.on('request', r => { const m = r.url().match(/\/assets\/(StageTable|VersionMatrix)-[^/]+\.js$/); if (m) chunks.push(m[1]) })
    await open(page, 'kardinal-test-app', 'default')
    await expect(page.locator('.station-map__svg')).toBeVisible()
    expect(chunks).toEqual([])
    await views(page).getByRole('radio', { name: 'View: Matrix' }).click()
    await expect(page.getByRole('region', { name: 'Versions by environment' })).toBeVisible()
    expect(chunks).toEqual(['VersionMatrix'])
  })

  test('the view switch is styled before the Table or the Matrix has loaded', async ({ page }) => {
    await open(page, 'kardinal-test-app', 'default')
    const map = views(page).getByRole('radio', { name: 'View: Map' })
    await expect(map).toBeVisible()
    const style = await map.evaluate(el => { const c = getComputedStyle(el); return { font: c.fontFamily, bg: c.backgroundColor } })
    expect(style.font).toMatch(/Archivo/)
    expect(style.bg).not.toBe('rgb(239, 239, 239)')
  })

  test('the view switch and the zoom control: one Tab stop each, arrow keys choose', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await open(page, 'kardinal-test-app', 'default')
    const map = views(page).getByRole('radio', { name: 'View: Map' })
    await expect(views(page).locator('[tabindex="0"]')).toHaveCount(1)
    await map.focus()
    await page.keyboard.press('ArrowRight')
    await expect(page).toHaveURL(/as=table/)
    await expect(views(page).getByRole('radio', { name: 'View: Table' })).toBeFocused()
    await page.keyboard.press('ArrowLeft')
    await expect(page.locator('.station-map__svg')).toBeVisible()
    await expect(page.getByRole('radio', { name: 'Zoom: Map' })).toBeVisible()
  })

  test("the Table's actions are the stage drawer's: Roll back is destructive and asks which version", async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await open(page, 'kardinal-test-app', 'default', '?as=table')
    const table = page.getByRole('region', { name: 'Environments' })
    const rollback = table.getByRole('row', { name: /^uat/ }).locator('[data-action="rollback"]')
    await expect(rollback).toHaveClass(/stage-button--destructive/)
    await rollback.click()
    const dialog = page.getByRole('dialog', { name: /^Roll uat back/ })
    await expect(dialog).toBeVisible()
    // The drawer's dialog: the version picker when there is a choice, and the hold option.
    await expect(dialog.getByRole('checkbox', { name: 'Hold uat on this version until released' })).toBeVisible()
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toHaveCount(0)
    await expect(rollback).toBeFocused()
  })

  test('the Matrix: arrow keys move between cells, which say their state in words', async ({ page }) => {
    await serveScale(page)
    await open(page, 'checkout-30', 'scale', '?as=matrix')
    const matrix = page.getByRole('region', { name: 'Versions by environment' })
    const first = matrix.locator('[data-cell="0:0"]')
    await first.focus()
    await page.keyboard.press('ArrowRight')
    await expect(matrix.locator('[data-cell="0:1"]')).toBeFocused()
    await expect(matrix.locator('[data-cell="0:1"]')).toHaveAttribute('aria-label', /^uat-\w: Live$/)
    await expect(page.locator('.version-matrix__status')).toHaveText(/^1\.4\.2 in uat-\w: Live$/)
    await page.keyboard.press('ArrowDown')
    await expect(matrix.locator('[data-cell="1:1"]')).toBeFocused()
  })

  // @perf: npm run test:e2e runs these alone (one worker). A shared CI runner cannot hold a
  // frame budget, so by default they assert what the page controls: the cells it draws (the
  // virtualizers' bound) and no long task while scrolling. KARDINAL_PERF_STRICT=1 (the perf
  // workflow, nightly) also asserts the frame-time p95, and holds long tasks to 100 ms (200 ms
  // otherwise: a loaded runner stretches tasks, 79 ms seen at load 37).
  const STRICT = process.env.KARDINAL_PERF_STRICT === '1'
  const LONG_TASK_MS = STRICT ? 100 : 200

  /** Scrolls the matrix diagonally one step a frame: the frame times' p95 (ms), the most cells
   *  drawn in any frame, and the longest task (ms) seen while scrolling. */
  async function diagonalScroll(matrix: import('@playwright/test').Locator): Promise<{ p95: number; cells: number; longest: number }> {
    return matrix.evaluate(async el => {
      el.scrollTop = 0
      el.scrollLeft = 0
      const tasks: number[] = []
      const obs = new PerformanceObserver(list => { for (const e of list.getEntries()) tasks.push(e.duration) })
      obs.observe({ type: 'longtask' })
      const times: number[] = []
      let cells = el.querySelectorAll('td[data-cell]').length
      let last = performance.now()
      for (let i = 0; i < 120; i++) {
        el.scrollTop += 37
        el.scrollLeft += 53
        await new Promise(r => requestAnimationFrame(r))
        const now = performance.now()
        times.push(now - last)
        last = now
        cells = Math.max(cells, el.querySelectorAll('td[data-cell]').length)
      }
      // Long tasks are reported after they end: one more frame, then stop.
      await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))
      tasks.push(...obs.takeRecords().map(e => e.duration))
      obs.disconnect()
      times.sort((a, b) => a - b)
      return { p95: times[Math.floor(times.length * 0.95)], cells, longest: Math.max(0, ...tasks) }
    })
  }

  /** The bounds every run asserts (at most maxCells drawn in any frame, no long task), and the
   *  frame budget when strict. */
  function expectScroll(r: { p95: number; cells: number; longest: number }, maxCells: number, budgetMs: number) {
    expect(r.cells, `cells drawn ${r.cells}`).toBeLessThan(maxCells)
    expect(r.longest, `longest task ${r.longest.toFixed(0)} ms`).toBeLessThanOrEqual(LONG_TASK_MS)
    if (STRICT) expect(r.p95, `frame p95 ${r.p95.toFixed(1)} ms`).toBeLessThanOrEqual(budgetMs)
  }

  test('200 versions: the default view scrolls with a bounded DOM (strict: 20 ms p95) @perf', async ({ page }) => {
    await serveScale(page)
    await page.setViewportSize({ width: 1440, height: 900 })
    await open(page, 'large-300', 'scale', '?as=matrix')
    const matrix = page.getByRole('region', { name: 'Versions by environment' })
    await expect(matrix.locator('tr[data-bundle]').first()).toBeVisible()
    // At 1440×900 the default view draws at most 280 cells of 200 × its columns.
    expectScroll(await diagonalScroll(matrix), 600, 20)
  })

  test('200 versions × 300 environments, every wave and fleet expanded: a bounded DOM (strict: 33 ms p95) @perf', async ({ page }) => {
    await serveScale(page)
    await page.setViewportSize({ width: 1440, height: 900 })
    await open(page, 'large-300', 'scale', '?as=matrix')
    const matrix = page.getByRole('region', { name: 'Versions by environment' })
    // The heads are virtualized too: expand the ones in view, scroll right, until none is left.
    await matrix.evaluate(el => { el.scrollLeft = 0 })
    for (let guard = 0; guard < 200; guard++) {
      const head = matrix.locator('thead button[aria-expanded="false"]').first()
      if (await head.count() > 0) {
        const name = (await head.textContent()) ?? ''
        await head.click()
        // Wait until that group is drawn as its members, so the next click is not on it again.
        await expect(matrix.locator('thead button[aria-expanded="false"]').filter({ hasText: name })).toHaveCount(0)
        continue
      }
      const moved = await matrix.evaluate(async el => {
        const before = el.scrollLeft
        el.scrollLeft += el.clientWidth / 2
        // Let the virtualizer draw the columns now in view before looking for heads.
        await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))
        return el.scrollLeft !== before
      })
      if (!moved && await matrix.locator('thead button[aria-expanded="false"]').count() === 0) break
    }
    await expect(matrix.locator('thead [aria-expanded="false"]')).toHaveCount(0)
    // The version column and all 300 environments.
    expect(await matrix.evaluate(el => el.querySelector('table')!.getAttribute('aria-colcount'))).toBe('301')
    // At 1440×900 it draws at most 980 of the 60,200 cells (narrow member columns mid-scroll).
    expectScroll(await diagonalScroll(matrix), 1200, 33)
  })

  test('the grid keeps a Tab stop when the focused cell scrolls out of view', async ({ page }) => {
    await serveScale(page)
    await open(page, 'large-300', 'scale', '?as=matrix')
    const matrix = page.getByRole('region', { name: 'Versions by environment' })
    await matrix.locator('[data-cell="0:0"]').focus()
    await matrix.evaluate(el => { el.scrollTop = el.scrollHeight })
    await expect(matrix.locator('td[data-cell][tabindex="0"]')).toHaveCount(1)
  })

  test('on a narrow screen, choosing the Map is kept in the link', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await open(page, 'kardinal-test-app', 'default')
    await views(page).getByRole('radio', { name: 'View: Map' }).click()
    await expect(page).toHaveURL(/as=map/)
    await page.reload()
    await expect(page.locator('.station-map__svg')).toBeVisible()
  })
})

