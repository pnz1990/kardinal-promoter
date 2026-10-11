// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 021: the version rail (DESIGN-SYSTEM §3.3). Versions are ticks on a time axis,
// newest on the right. Hovering a tick previews its route on the map as a ghost thread;
// [ and ] follow an older or newer version and f the active one; c on a focused tick
// compares it, and the comparison panel carries the map's marks, a row per environment.
// On a narrow map the zoom level control gives way to zoom buttons.

import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { serveScale } from './scaleFixtures'

const rail = (page: import('@playwright/test').Page) => page.getByRole('toolbar', { name: /^Versions/ })
const tick = (page: import('@playwright/test').Page, name: string) => rail(page).locator(`.bundle-chip[title^="${name}:"]`)

test.describe('Journey 021 — Version rail', () => {
  test.beforeEach(async ({ page }) => {
    await serveScale(page)
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/ui/#/p/scale/checkout-30')
    await expect(page.locator('.station-map__svg [data-station]').first()).toBeVisible()
  })

  test('ticks run oldest to newest; hovering one previews its route as a ghost thread', async ({ page }) => {
    await expect(rail(page).locator('.bundle-chip')).toHaveCount(2)
    await expect(rail(page).locator('.bundle-chip').nth(1)).toHaveAttribute('aria-pressed', 'true')
    await expect(page.locator('.station-map__thread--ghost path')).toHaveCount(0)
    await tick(page, 'checkout-30-6').hover()
    await expect(page.locator('.station-map__thread--ghost path').first()).toBeAttached()
    await page.mouse.move(0, 0)
    await expect(page.locator('.station-map__thread--ghost')).toHaveCount(0)
  })

  test('[ and ] follow an older and a newer version, f the active one', async ({ page }) => {
    await page.locator('body').click({ position: { x: 1, y: 1 } })
    await page.keyboard.press('[')
    await expect(tick(page, 'checkout-30-6')).toHaveAttribute('aria-pressed', 'true')
    await page.keyboard.press(']')
    await expect(tick(page, 'checkout-30-7')).toHaveAttribute('aria-pressed', 'true')
    await page.keyboard.press('[')
    await page.keyboard.press('f')
    await expect(tick(page, 'checkout-30-7')).toHaveAttribute('aria-pressed', 'true')
  })

  test('c on a focused tick opens the comparison, with the map\'s marks and a row per environment', async ({ page }) => {
    await tick(page, 'checkout-30-6').focus()
    await page.keyboard.press('c')
    const panel = page.getByRole('dialog', { name: 'Version comparison' })
    await expect(panel).toBeVisible()
    await expect(page).toHaveURL(/compare=checkout-30-6/)
    await expect(panel.locator('.bundle-diff__col-head')).toHaveText(['Following', 'Compared'])
    // edge-06 failed in 7 and is live in 6: its row is marked.
    await expect(panel.locator('.bundle-diff__row--env').filter({ hasText: 'edge-06' }).locator('.bundle-diff__env')).toHaveText('≠ edge-06')
    const axe = await new AxeBuilder({ page }).include('.bundle-diff').withTags(['wcag2a', 'wcag2aa']).analyze()
    expect(axe.violations.map(v => `${v.id}: ${v.nodes[0]?.target[0]}`)).toEqual([])
    await page.keyboard.press('Escape')
    await expect(panel).toHaveCount(0)
  })

  test('every tick shares the glyph line, and the axis runs under the glyphs, never through a name', async ({ page }) => {
    await page.goto('/ui/#/p/scale/large-300')
    await expect(rail(page).locator('.bundle-chip')).toHaveCount(10)
    const boxes = await rail(page).evaluate(el => {
      const axis = el.querySelector('.bundle-timeline__axis')!.getBoundingClientRect()
      return {
        axis: axis.top,
        ticks: [...el.querySelectorAll('.bundle-chip')].map(c => ({
          dot: c.querySelector('.bundle-chip__dot')!.getBoundingClientRect(),
          name: c.querySelector('.bundle-chip__name')!.getBoundingClientRect(),
          tick: c.getBoundingClientRect(),
        })),
      }
    })
    const first = boxes.ticks[0]
    for (const t of boxes.ticks) {
      expect(Math.abs(t.dot.top - first.dot.top)).toBeLessThan(1)
      expect(Math.abs(t.tick.bottom - first.tick.bottom)).toBeLessThan(1)
      expect(boxes.axis).toBeGreaterThanOrEqual(t.dot.bottom - 0.5)
      expect(boxes.axis).toBeLessThanOrEqual(t.name.top + 0.5)
    }
    // The followed tick says so in words.
    await expect(rail(page).locator('.bundle-chip[aria-pressed="true"] .bundle-chip__phase')).toHaveText(/^Following · /)
  })

  test('at 390 px with 10 versions the followed tick is on screen, at load and after f', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto('/ui/#/p/scale/large-300?as=map')
    const bar = page.locator('.bundle-timeline__scroll')
    await expect(rail(page).locator('.bundle-chip')).toHaveCount(10)
    // The rail is wider than the screen: the newest versions start past its right edge.
    expect(await bar.evaluate(el => el.scrollWidth > el.clientWidth)).toBe(true)
    const title = page.getByText('Versions (oldest → newest)')
    const inView = (sel: string) => bar.evaluate((el, sel) => {
      const c = el.getBoundingClientRect(), r = el.querySelector(sel)!.getBoundingClientRect()
      return r.left >= c.left - 0.5 && r.right <= c.right + 0.5
    }, sel)
    const followed = '.bundle-chip[aria-pressed="true"]'
    await expect(tick(page, 'large-300-7')).toHaveAttribute('aria-pressed', 'true')
    await expect.poll(() => inView(followed)).toBe(true)
    // Only the ticks scroll: the title stays on screen.
    await expect(title).toBeInViewport()
    // Follow the oldest shown (scrolled to it), then f: the active version comes back into view.
    await bar.evaluate(el => { el.scrollLeft = 0 })
    await rail(page).locator('.bundle-chip').first().click()
    await expect(rail(page).locator('.bundle-chip').first()).toHaveAttribute('aria-pressed', 'true')
    await expect.poll(() => inView(followed)).toBe(true)
    await page.locator('body').click({ position: { x: 1, y: 1 } })
    await page.keyboard.press('f')
    await expect(tick(page, 'large-300-7')).toHaveAttribute('aria-pressed', 'true')
    await expect.poll(() => inView(followed)).toBe(true)
  })

  test('a narrow map has zoom buttons in place of the level control', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.getByRole('radio', { name: 'View: Map' }).click()
    const zoom = page.getByRole('group', { name: 'Zoom in or out' })
    await expect(zoom).toBeVisible()
    await expect(page.locator('.station-map').getByRole('radiogroup', { name: 'Zoom' })).toBeHidden()
    const svg = page.locator('.station-map__svg')
    const before = await svg.getAttribute('viewBox')
    await zoom.getByRole('button', { name: 'Zoom in' }).click()
    await expect.poll(() => svg.getAttribute('viewBox')).not.toBe(before)
    await zoom.getByRole('button', { name: 'Fit the map' }).click()
  })
})
