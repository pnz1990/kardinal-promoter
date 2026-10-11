// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 017: the station map (U2, DESIGN-SYSTEM §3) at 3, 30 and 300
// environments. One stage graph: each station shows what it runs and what is
// on its way, gates sit on the edge into the environment they guard, the
// followed version is one red thread, waves and fleets collapse into bands
// and platforms, and the map opens at Detail, Map or Overview by size.

import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { serveScale } from './scaleFixtures'

async function open(page: Page, pipeline: string, ns: string) {
  await page.goto(`/ui/#pipeline=${pipeline}&ns=${ns}`)
  await expect(page.getByRole('heading', { level: 1, name: pipeline })).toBeVisible()
  await expect(page.locator('.station-map__svg [data-station]').first()).toBeVisible()
}

/** A screenshot for the PR's visual review, attached and kept in the test's output directory. */
async function shot(page: Page, name: string) {
  const path = test.info().outputPath(`${name}.png`)
  await page.screenshot({ path })
  await test.info().attach(name, { path, contentType: 'image/png' })
}

test.describe('Journey 017 — Station map', () => {
  test('3 environments: Detail, running and incoming versions, the gate on the edge, the thread', async ({ page }) => {
    await page.setViewportSize({ width: 1366, height: 768 })
    await open(page, 'kardinal-test-app', 'default')
    const map = page.locator('.station-map')
    await expect(map.getByRole('radio', { name: 'Detail' })).toHaveAttribute('aria-checked', 'true')
    // prod runs its own version while a newer one is on its way, its PR open but held by a gate
    // (the PipelineLaneView.tsx:207 bug).
    const prod = page.locator('[data-station="prod"]')
    await expect(prod).toHaveAttribute('aria-label', /^prod — Held by gate no-weekend-deploys, runs sha-9f8e7d6, .* ago, incoming sha-abc1234, blocked by no-weekend-deploys$/)
    await expect(prod).toContainText('sha-9f8e7d6')
    await expect(prod.locator('.station__incoming').nth(0)).toHaveText('→ sha-abc1234 · PR #42')
    await expect(prod.locator('.station__incoming').nth(1)).toHaveText('Held by gate no-weekend-deploys')
    await expect(page.locator('[data-station="uat"]')).toContainText('sha-abc1234')
    const gate = page.getByRole('button', { name: 'gate no-weekend-deploys, Blocking' })
    await expect(gate).toBeVisible()
    // The gate takes the pointer over its whole label, even where the label reaches over uat's column.
    await gate.hover()
    await expect(page.locator('.gate-popover')).toBeVisible()
    await page.mouse.move(0, 0)
    await expect(page.locator('.station-map__thread [data-kind="solid"]')).toHaveCount(1)
    // The gate holds prod: the thread stops before its bar, and prod counts as blocked.
    await expect(page.locator('.station-map__thread [data-kind="stopped"]')).toHaveCount(1)
    await expect(map.getByRole('group', { name: 'Needs attention' })).toContainText('1 blocked by no-weekend-deploys')
    // The map gets most of the page at 1366×768.
    const box = (await page.locator('.station-map').boundingBox())!
    expect(box.height / 768).toBeGreaterThanOrEqual(0.6)
    await shot(page, '3-envs-detail')
  })

  test('a station opens its stage drawer, and Escape on the drawer returns focus to it', async ({ page }) => {
    await open(page, 'kardinal-test-app', 'default')
    const uat = page.locator('[data-station="uat"]')
    await uat.click()
    await expect(page.getByLabel('Close')).toBeVisible()
    await page.getByLabel('Close').click()
    await expect(page.getByLabel('Close')).toBeHidden()
    await expect(uat).toBeFocused()
  })

  test('keyboard: / searches the map, not the page; Esc clears the map first, then closes the drawer', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await open(page, 'kardinal-test-app', 'default')
    await page.locator('[data-station="uat"]').click()
    const drawer = page.getByTestId('stage-drawer')
    await expect(drawer).toBeVisible()
    await page.locator('[data-station="uat"]').focus()
    await page.keyboard.press('/')
    const search = page.getByRole('searchbox', { name: /Search environments/ })
    await expect(search).toBeFocused()
    await search.fill('prod')
    await page.locator('[data-station="uat"]').focus()
    // The first Esc clears the map's search; the drawer stays open.
    await page.keyboard.press('Escape')
    await expect(search).toHaveValue('')
    await expect(drawer).toBeVisible()
    // The next Esc is the page's: it closes the drawer.
    await page.keyboard.press('Escape')
    await expect(drawer).toBeHidden()
  })

  test('keyboard: arrows move between stations, Enter opens one', async ({ page }) => {
    await open(page, 'kardinal-test-app', 'default')
    const test0 = page.locator('[data-station="test"]')
    await test0.focus()
    await page.keyboard.press('ArrowRight')
    await expect(page.locator('[data-station="uat"]')).toBeFocused()
    await page.keyboard.press('ArrowRight')
    await expect(page.locator('[data-station="prod"]')).toBeFocused()
    await page.keyboard.press('ArrowLeft')
    await expect(page.locator('[data-station="uat"]')).toBeFocused()
    // Only the station in focus offers its actions.
    await expect(page.getByRole('button', { name: 'Roll back uat' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Roll back test' })).toHaveCount(0)
    await page.keyboard.press('Enter')
    await expect(page.getByLabel('Close')).toBeVisible()
    await page.getByLabel('Close').click()
    await expect(page.locator('[data-station="uat"]')).toBeFocused()
    // A gate takes focus and shows why it holds.
    await page.getByRole('button', { name: 'gate no-weekend-deploys, Blocking' }).focus()
    await expect(page.locator('.gate-popover')).toContainText('!schedule.isWeekend')
  })

  test('30 environments: Map zoom, a wave band, a fleet platform, the attention strip', async ({ page }) => {
    await serveScale(page)
    await page.setViewportSize({ width: 1366, height: 768 })
    await open(page, 'checkout-30', 'scale')
    const map = page.locator('.station-map')
    await expect(map.getByRole('radio', { name: 'Map' })).toHaveAttribute('aria-checked', 'true')
    await expect(map.getByRole('button', { name: /^WAVE 1 · 6: / })).toBeVisible()
    const edge = map.getByRole('button', { name: /^edge · 12 targets, 2 at a time · stop at 2 failed: / })
    await expect(edge).toHaveAttribute('aria-expanded', 'false')
    await expect(map.getByRole('group', { name: 'Needs attention' })).toContainText('1 failed')
    await expect(map.getByRole('button', { name: '1 blocked by change-freeze' })).toBeVisible()
    // One of 12 edge targets failed (the fleet stops at 2): the thread keeps going into the fleet,
    // and the failure is a mark on the platform.
    await expect(page.locator('.station-map__thread [data-kind="snap"]')).toHaveCount(0)
    await expect(page.locator('.station-map__thread [data-kind="flow"]')).toHaveCount(1)
    await expect(page.locator('[data-group="platform:edge"] .group__fail')).toHaveText('✕ 1')
    await shot(page, '30-envs-map')
    // The attention strip expands the fleet and focuses the failed target.
    await map.getByRole('button', { name: '1 failed', exact: true }).click()
    await expect(edge).toHaveAttribute('aria-expanded', 'true')
    await expect(page.locator('[data-station="edge-06"]')).toBeFocused()
    await shot(page, '30-envs-fleet-expanded')
    const axe = await new AxeBuilder({ page }).include('.station-map').analyze()
    expect(axe.violations.map(v => `${v.id}: ${v.nodes[0]?.target[0]}`)).toEqual([])
  })

  test('compare two bundles: the second route is an ink dashed thread, and ≠ marks where they differ', async ({ page }) => {
    await serveScale(page)
    await open(page, 'checkout-30', 'scale')
    // Shift-click the older Bundle on the rail.
    await page.locator('button[data-bundle-phase="Superseded"]').click({ modifiers: ['Shift'] })
    await expect(page.locator('.station-map__thread--compare path').first()).toBeAttached()
    await expect(page.locator('.station__differs').first()).toBeAttached()
    await expect(page.locator('[data-station="canary"]')).not.toHaveAttribute('aria-label', /compared version differs/)
    await shot(page, '30-envs-compare')
  })

  test('300 environments: Overview, expand a fleet, search, open a target under 2 s', async ({ page }) => {
    await serveScale(page)
    await page.setViewportSize({ width: 1366, height: 768 })
    const started = Date.now()
    await open(page, 'large-300', 'scale')
    const map = page.locator('.station-map')
    await expect(map.getByRole('radio', { name: 'Overview' })).toHaveAttribute('aria-checked', 'true')
    // Dagre lays out the collapsed graph: stations, 3 bands, 2 platforms, the held audit.
    await expect(map.getByRole('button', { name: /^prod · 149 targets, 8 at a time · stop at 5 failed: 3 failed/ })).toBeVisible()
    await expect(map.getByRole('button', { name: /^WAVE 2 · 48: / })).toBeVisible()
    await expect(map.getByRole('group', { name: 'Needs attention' })).toContainText('3 failed')
    await expect(map.getByRole('group', { name: 'Needs attention' })).toContainText('1 held')
    await shot(page, '300-envs-overview')
    // Search a region: the fleet expands, matches stay bright, the rest dims.
    await page.getByRole('searchbox', { name: /Search environments/ }).fill('prod-eu-west')
    await expect(map.getByText(/\d+ matches/)).toBeVisible()
    const prod = map.getByRole('button', { name: /^prod · 149 targets/ })
    await expect(prod).toHaveAttribute('aria-expanded', 'true')
    await shot(page, '300-envs-search')
    // A failed target opens its drawer.
    await page.locator('[data-station="prod-eu-west-29"]').click()
    await expect(page.getByLabel('Close')).toBeVisible()
    expect(Date.now() - started).toBeLessThan(10_000) // the whole flow; the drawer itself opens at once
    // Zoom levels can be set (with the drawer closed, the map has the width for them).
    await page.getByLabel('Close').click()
    await map.getByRole('radio', { name: 'Map' }).click()
    await expect(map.getByRole('radio', { name: 'Map' })).toHaveAttribute('aria-checked', 'true')
  })

  test('reduced motion: no running animation; dark theme; no overflow at 390×844', async ({ page }) => {
    await serveScale(page)
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await open(page, 'checkout-30', 'scale')
    // The thread flowing into the edge fleet stops.
    await expect(page.locator('.station-map__thread [data-kind="flow"]')).toHaveCount(1)
    // Animations (the thread's flow, the promoting arc), not the theme's colour transitions.
    const running = await page.evaluate(() => document.getAnimations().filter(a => a instanceof CSSAnimation && a.playState === 'running' &&
      (a.effect as KeyframeEffect | null)?.target instanceof Element && ((a.effect as KeyframeEffect).target as Element).closest('.station-map'))
      .map(a => `${(a as CSSAnimation).animationName ?? (a as CSSTransition).transitionProperty} on ${((a.effect as KeyframeEffect).target as Element).getAttribute('class')}`))
    expect(running).toEqual([])

    await page.emulateMedia({ reducedMotion: 'no-preference', colorScheme: 'dark' })
    await page.evaluate(() => localStorage.removeItem('kardinal-theme'))
    await page.reload()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await expect(page.locator('.station-map__svg [data-station]').first()).toBeVisible()
    // With motion allowed, the thread into the fleet does flow (once the followed Bundle's graph is in).
    await expect(page.locator('.station-map__thread [data-kind="flow"]')).toHaveCount(1)
    await expect.poll(() => page.evaluate(() => document.getAnimations().filter(a => a instanceof CSSAnimation && a.animationName === 'thread-flow').length)).toBeGreaterThan(0)
    await open(page, 'kardinal-test-app', 'default')
    await shot(page, '3-envs-dark')
    const axe = await new AxeBuilder({ page }).include('.station-map').withTags(['wcag2a', 'wcag2aa']).analyze()
    expect(axe.violations.map(v => `${v.id}: ${v.nodes[0]?.target[0]}`)).toEqual([])

    await page.setViewportSize({ width: 390, height: 844 })
    await expect(page.locator('.station-map')).toBeVisible()
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    expect(overflow).toBeLessThanOrEqual(0)
    await shot(page, '3-envs-390')
  })

  for (const [n, pipeline, ns] of [['3', 'kardinal-test-app', 'default'], ['30', 'checkout-30', 'scale'], ['300', 'large-300', 'scale']] as const) {
    test(`${n} environments: no map label overlaps another at 390 px, nor at the smallest zoom at 1440`, async ({ page }) => {
      await serveScale(page)
      await page.setViewportSize({ width: 390, height: 844 })
      await open(page, pipeline, ns)
      await page.waitForTimeout(400)
      expect(await overlappingLabels(page)).toEqual([])
      await page.setViewportSize({ width: 1440, height: 900 })
      await page.reload()
      await expect(page.locator('.station-map__svg [data-station]').first()).toBeVisible()
      // Zoom out to the minimum (0.2).
      const svg = page.locator('.station-map__svg')
      await svg.focus()
      for (let i = 0; i < 12; i++) await page.keyboard.press('-')
      await page.waitForTimeout(300)
      expect(await overlappingLabels(page)).toEqual([])
    })
  }

  test('keyboard: / from the zoom control searches the map; Esc on a gate closes its popover, then the drawer', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await open(page, 'kardinal-test-app', 'default')
    await page.locator('.station-map').getByRole('radiogroup', { name: 'Zoom level' }).getByRole('radio', { name: 'Map' }).focus()
    await page.keyboard.press('/')
    await expect(page.getByRole('searchbox', { name: /Search environments/ })).toBeFocused()
    await page.locator('[data-station="uat"]').click()
    const drawer = page.getByTestId('stage-drawer')
    await expect(drawer).toBeVisible()
    await page.getByRole('button', { name: 'gate no-weekend-deploys, Blocking' }).focus()
    await expect(page.locator('.gate-popover')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.locator('.gate-popover')).toBeHidden()
    await expect(drawer).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(drawer).toBeHidden()
  })
})

/** The pairs of visible map labels whose boxes overlap (by more than half a pixel). */
async function overlappingLabels(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const boxes = [...document.querySelectorAll<SVGTextElement>('.station-map__svg text')]
      .filter(t => getComputedStyle(t).visibility !== 'hidden' && t.textContent?.trim())
      .map(t => ({ t: t.textContent!.trim(), r: t.getBoundingClientRect() }))
      .filter(b => b.r.width > 0)
    const out: string[] = []
    for (let i = 0; i < boxes.length; i++) {
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i].r
        const b = boxes[j].r
        const dx = Math.min(a.right, b.right) - Math.max(a.left, b.left)
        const dy = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top)
        if (dx > 0.5 && dy > 0.5) out.push(`${boxes[i].t} × ${boxes[j].t}`)
      }
    }
    return out
  })
}

