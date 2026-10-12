// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 022: the map's toolbar responds to the map's own width (a container query), not the
// viewport. With the stage drawer open at 1280 the map is about 480 px wide, as on a phone:
// attention takes a row of its own, each item on one line (cut with an ellipsis, the full text
// its title), then search and the View and Zoom groups, each group whole and labelled. Nothing
// overlaps.

import { test, expect, type Page } from '@playwright/test'
import { serveScale } from './scaleFixtures'

interface Box { name: string; left: number; right: number; top: number; bottom: number }

/** The bar's controls (each attention item, search, View, Zoom) and the items' line counts. */
async function measure(page: Page) {
  return page.locator('.station-map__bar').evaluate(bar => {
    const box = (name: string, el: Element): Box => {
      const r = el.getBoundingClientRect()
      return { name, left: r.left, right: r.right, top: r.top, bottom: r.bottom }
    }
    const visible = (el: Element) => (el as HTMLElement).offsetParent !== null
    const boxes: Box[] = []
    bar.querySelectorAll('.station-map__attention-item').forEach((el, i) => boxes.push(box(`attention ${i}`, el)))
    const search = bar.querySelector('.station-map__search input')
    if (search) boxes.push(box('search', search))
    bar.querySelectorAll('[role="radiogroup"], [role="group"]:not(.station-map__attention)').forEach(el => {
      if (visible(el)) boxes.push(box(el.getAttribute('aria-label') ?? 'group', el))
    })
    const items = [...bar.querySelectorAll<HTMLElement>('.station-map__attention-item')].map(el => {
      const line = parseFloat(getComputedStyle(el).lineHeight) || parseFloat(getComputedStyle(el).fontSize) * 1.5
      return { text: el.textContent ?? '', lines: Math.round(el.getBoundingClientRect().height / line), title: el.title }
    })
    const r = bar.getBoundingClientRect()
    return { boxes, items, bar: { left: r.left, right: r.right } }
  })
}

function expectTidy(m: Awaited<ReturnType<typeof measure>>) {
  // Every control inside the bar, none overlapping another.
  for (const b of m.boxes) {
    expect(b.left, `${b.name} inside the bar`).toBeGreaterThanOrEqual(m.bar.left - 0.5)
    expect(b.right, `${b.name} inside the bar`).toBeLessThanOrEqual(m.bar.right + 0.5)
  }
  for (const [i, a] of m.boxes.entries()) {
    for (const b of m.boxes.slice(i + 1)) {
      const overlap = a.left < b.right - 0.5 && b.left < a.right - 0.5 && a.top < b.bottom - 0.5 && b.top < a.bottom - 0.5
      expect(overlap, `${a.name} overlaps ${b.name}`).toBe(false)
    }
  }
  // Each attention item on one line, its full text in its title.
  expect(m.items.length).toBeGreaterThan(0)
  for (const it of m.items) {
    expect(it.lines, `"${it.text}" on one line`).toBe(1)
    expect(it.title).toBe(it.text)
  }
}

test.describe('Journey 022 — The map toolbar on a narrow map', () => {
  test('with the stage drawer open at 1280, attention has its own row and nothing overlaps', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 })
    await page.goto('/ui/#/p/default/kardinal-test-app/env/prod')
    await expect(page.getByTestId('stage-drawer')).toBeVisible()
    const map = page.locator('.station-map')
    expect((await map.boundingBox())!.width).toBeLessThanOrEqual(720)
    const m = await measure(page)
    expectTidy(m)
    // Attention sits above the other controls.
    const attention = m.boxes.filter(b => b.name.startsWith('attention'))
    const rest = m.boxes.filter(b => !b.name.startsWith('attention'))
    for (const a of attention) for (const b of rest) expect(a.bottom).toBeLessThanOrEqual(b.top + 0.5)
    // View and Zoom keep their labels; the zoom buttons have names, and Fit draws an icon.
    const bar = page.locator('.station-map__bar')
    await expect(bar.locator('.segmented__label').filter({ hasText: 'View' })).toBeVisible()
    await expect(bar.locator('.station-map__zoom-label')).toHaveText('Zoom')
    const zoom = page.getByRole('group', { name: 'Zoom in or out' })
    for (const name of ['Zoom out', 'Zoom in', 'Fit the map']) await expect(zoom.getByRole('button', { name })).toBeVisible()
    await expect(zoom.getByRole('button', { name: 'Fit the map' }).locator('svg path')).toBeAttached()
  })

  test('at 390 px, and with many attention items, each stays on one line', async ({ page }) => {
    await serveScale(page)
    await page.setViewportSize({ width: 390, height: 844 })
    for (const url of ['/ui/#/p/default/kardinal-test-app?as=map', '/ui/#/p/scale/large-300?as=map', '/ui/#/p/scale/checkout-30?as=map']) {
      await page.goto(url)
      await expect(page.locator('.station-map__attention-item').first()).toBeVisible()
      expectTidy(await measure(page))
    }
  })

  test('a wide map keeps one row', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/ui/#/p/default/kardinal-test-app')
    const m = await measure(page)
    expectTidy(m)
    const tops = new Set(m.boxes.map(b => Math.round((b.top + b.bottom) / 2 / 8)))
    expect(tops.size).toBe(1)
    await expect(page.getByRole('group', { name: 'Zoom in or out' })).toBeHidden()
  })
})
