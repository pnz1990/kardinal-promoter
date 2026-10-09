// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-TIMELINE-01, UI-BUNDLETYPE-01 and part of UI-KEYBOARD-01: the bundle timeline (select, shift-click and Compare) and
// the comparison panel. Go test: TestUI_BrowserTimeline
// (test/e2e/live/ui_browser_test.go).
//
// podinfo in KARDINAL_UI_NAMESPACE has two Bundles, both promoted to its one
// environment, test. KARDINAL_UI_BUNDLES describes them, oldest first: their
// phase, image, author, commit SHA, environments (as the panel prints them)
// and the name of their test step.

import { test, expect, type Locator, type Page } from '@playwright/test'
import { need, needJSON, openPipeline, dagNode, PIPELINE } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')

interface Entry { name: string; phase: string; image: string; author: string; commitSHA: string; environments: string; step: string }
const [older, newer] = needJSON<Entry[]>('KARDINAL_UI_BUNDLES')

const chips = (page: Page) => page.locator('.bundle-chip')
const chip = (page: Page, b: Entry) => page.locator(`.bundle-chip[title^="${b.name}: "]`)
const comparison = (page: Page) => page.getByRole('dialog', { name: 'Bundle comparison' })
const hashParams = (page: Page) => new URLSearchParams(new URL(page.url()).hash.slice(1))

/** expectShown checks that b is the Bundle on screen: chip, provenance, DAG. */
async function expectShown(page: Page, b: Entry, other: Entry) {
  await expect(chip(page, b)).toHaveAttribute('aria-pressed', 'true')
  await expect(chip(page, other)).toHaveAttribute('aria-pressed', 'false')
  await expect(page.getByRole('button', { name: `Copy bundle name "${b.name}"` })).toBeVisible()
  await expect(page.getByTitle('Author', { exact: true })).toHaveText(b.author)
  await expect(dagNode(page, 'test')).toHaveAttribute('data-node-id', b.step)
}

/** expectComparison checks the panel for Bundle A a and Bundle B b. */
async function expectComparison(panel: Locator, a: Entry, b: Entry) {
  await expect(panel.getByRole('heading', { name: 'Bundle Comparison' })).toBeVisible()
  const rows = panel.locator('div[style*="grid-template-columns"]')
  const row = (label: string) => rows.filter({ hasText: new RegExp(`^(▸ )?${label}`) })
  const cells = (label: string) => row(label).locator(':scope > span')

  await expect(cells('Name').nth(1)).toHaveAttribute('title', a.name)
  await expect(cells('Name').nth(2)).toHaveAttribute('title', b.name)

  // [label, A, B]: what the panel prints for each field.
  const fields: [string, string, string][] = [
    ['Images', a.image, b.image],
    ['Environments', a.environments, b.environments],
    ['Phase', a.phase, b.phase],
    ['Type', 'image', 'image'],
    ['Author', a.author, b.author],
    ['Commit SHA', a.commitSHA.slice(0, 12), b.commitSHA.slice(0, 12)],
    ['CI Run', '—', '—'],
  ]
  let changed = 0
  for (const [label, va, vb] of fields) {
    const differs = va !== vb
    if (differs) changed++
    await expect(cells(label).nth(0)).toHaveText(differs ? `▸ ${label}` : label)
    await expect(cells(label).nth(1)).toHaveText(va)
    await expect(cells(label).nth(2)).toHaveText(vb)
  }
  // Created is shown, and never counts as a difference.
  await expect(cells('Created').nth(0)).toHaveText('Created')
  expect(changed).toBeGreaterThanOrEqual(3)
  await expect(panel).toContainText(`${changed} fields differ between these bundles`)
}

test('the timeline selects a Bundle, and shift-click compares two', async ({ page }) => {
  await openPipeline(page, base, ns)
  await expect(page.getByText('Bundle History (newest → oldest)')).toBeVisible()
  await expect(page.getByText('Shift-click to compare', { exact: true })).toBeVisible()

  // Newest first, each with its phase; the newest is on screen, so only the
  // other one offers a shift-click.
  await expect(chips(page)).toHaveCount(2)
  for (const [i, b] of [newer, older].entries()) {
    const c = chips(page).nth(i)
    await expect(c).toHaveAttribute('title', `${b.name}: ${b.phase}${b === newer ? '' : '\nShift-click to compare'}`)
    await expect(c).toHaveAttribute('data-bundle-phase', b.phase)
    await expect(c).toHaveClass(new RegExp(`bundle-chip--${b.phase.toLowerCase()}`))
    await expect(c.locator('.bundle-chip__name')).toHaveText(b.name.split('-').pop()!)
  }
  await expectShown(page, newer, older)

  // UI-BUNDLETYPE-01: the Bundle card names the type; image Bundles, the
  // common case, carry no badge in the timeline.
  await expect(page.getByRole('img', { name: 'image Bundle' })).toBeVisible()
  const bar = page.getByRole('toolbar', { name: /Bundle history/ })
  await expect(bar.getByRole('img')).toHaveCount(0)
  // UI-KEYBOARD-01: the timeline is one Tab stop, the arrow keys move in it.
  await expect(bar.locator('.bundle-chip[tabindex="0"]')).toHaveCount(1)
  await chip(page, newer).focus()
  await page.keyboard.press('ArrowRight')
  await expect(chip(page, older)).toBeFocused()
  await page.keyboard.press('ArrowLeft')
  await expect(chip(page, newer)).toBeFocused()
  await expect(bar.locator('.bundle-chip[tabindex="0"]')).toHaveCount(1)

  // A click shows the older Bundle, and it stays on screen across polls.
  await chip(page, older).click()
  await expectShown(page, older, newer)
  await page.waitForResponse(r => r.url().includes(`/api/v1/ui/pipelines/${PIPELINE}/bundles`) && r.ok())
  await page.waitForResponse(r => r.url().includes(`/api/v1/ui/pipelines/${PIPELINE}/bundles`) && r.ok())
  await expectShown(page, older, newer)

  // A shift-click on the Bundle on screen does nothing: it is Bundle A.
  await chip(page, older).click({ modifiers: ['Shift'] })
  await expect(chip(page, older)).not.toHaveClass(/bundle-chip--compare/)
  await expect(page.getByRole('button', { name: '× clear' })).toHaveCount(0)
  await expect(page.getByText('Shift-click to compare', { exact: true })).toBeVisible()

  // Shift-click the newer one: it becomes Bundle B, and Compare and clear
  // show. Compare opens the comparison, the shown Bundle as A.
  await expect(comparison(page)).toHaveCount(0)
  await chip(page, newer).click({ modifiers: ['Shift'] })
  await expect(chip(page, newer)).toHaveClass(/bundle-chip--compare/)
  await expect(chip(page, newer)).toContainText('⇅B')
  await expect(comparison(page)).toHaveCount(0)
  expect(hashParams(page).get('bundle')).toBeNull()
  const compare = page.getByRole('button', { name: 'Compare ↔' })
  const clear = page.getByRole('button', { name: '× clear' })
  await expect(clear).toBeVisible()
  await compare.click()
  const panel = comparison(page)
  await expect(panel).toBeVisible()
  await expectComparison(panel, older, newer)
  expect(hashParams(page).get('bundle')).toBe(newer.name)

  // Closing keeps Bundle B and puts focus back on Compare, so the keyboard
  // opens it again; Esc closes it.
  await panel.getByRole('button', { name: 'Close comparison' }).click()
  await expect(panel).toHaveCount(0)
  await expect.poll(() => hashParams(page).get('bundle')).toBeNull()
  await expect(chip(page, newer)).toHaveClass(/bundle-chip--compare/)
  await expect(compare).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(panel).toBeVisible()
  await expect(panel.getByRole('button', { name: 'Close comparison' })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(panel).toHaveCount(0)
  await expect(compare).toBeFocused()

  // clear drops Bundle B.
  await clear.click()
  await expect(chip(page, newer)).not.toHaveClass(/bundle-chip--compare/)
  await expect(compare).toHaveCount(0)
  await expect(page.getByText('Shift-click to compare', { exact: true })).toBeVisible()
  expect(hashParams(page).get('bundle')).toBeNull()
  await expectShown(page, older, newer)
})

test('a link with bundle= opens the comparison with the Bundle on screen', async ({ page }) => {
  await openPipeline(page, base, ns, PIPELINE, { bundle: older.name })
  const panel = comparison(page)
  await expect(panel).toBeVisible()
  await expectComparison(panel, newer, older)
  await page.keyboard.press('Escape')
  await expect(panel).toHaveCount(0)
  await expect.poll(() => hashParams(page).get('bundle')).toBeNull()
  await expectShown(page, newer, older)
  // The linked Bundle stays Bundle B, as after Compare.
  await expect(chip(page, older)).toHaveClass(/bundle-chip--compare/)
  await expect(page.getByRole('button', { name: 'Compare ↔' })).toBeVisible()
})
