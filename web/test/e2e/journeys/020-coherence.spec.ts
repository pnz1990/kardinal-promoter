// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Journey 020 — one way to say things across the shell and the map: a version is its tag
// everywhere (the Bundle name only as quiet secondary text), the top bar keeps the full
// width when the stage drawer opens, and the comparison marks changes without the hold tone.
import { test, expect } from '@playwright/test'

test.describe('Journey 020 — Coherence', () => {
  test('a version reads as its tag on the rail, in the header, the Versions tab and the sidebar', async ({ page }) => {
    await page.goto('/ui/#/p/default/kardinal-test-app')
    const rail = page.getByRole('toolbar', { name: /^Versions/ })
    await expect(rail.locator('.bundle-chip__name')).toHaveText(['sha-abc1234', 'config 77aa001', 'sha-9f8e7d6'])
    const header = page.locator('.app-pipeline-head__summary')
    await expect(header.locator('.version-label__tag')).toHaveText('sha-abc1234')
    await expect(header.locator('.version-label')).toHaveAttribute('title', 'kardinal-test-app-abc123')
    await expect(page.locator('#app-sidebar, .app-sidebar').getByText('sha-abc1234', { exact: true })).toBeVisible()
    await page.getByRole('tab', { name: /^Versions/ }).click()
    await expect(page.getByRole('list', { name: 'Versions' }).locator('.version-label__tag').first()).toHaveText('sha-abc1234')
  })

  test('the stage drawer opens below a full-width top bar', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/ui/#/p/default/kardinal-test-app/env/prod')
    const drawer = page.getByTestId('stage-drawer')
    await expect(drawer).toBeVisible()
    const bar = (await page.locator('.app-topbar').boundingBox())!
    const box = (await drawer.boundingBox())!
    expect(box.y).toBeGreaterThanOrEqual(bar.y + bar.height - 1)
    expect(bar.x + bar.width).toBeGreaterThanOrEqual(1440 - 1)
    // One line: the breadcrumb does not wrap.
    const crumbs = (await page.getByRole('navigation', { name: 'Breadcrumb' }).boundingBox())!
    expect(crumbs.height).toBeLessThan(bar.height)
  })

  test('the comparison marks changed rows without the hold tone, and links fit their column', async ({ page }) => {
    await page.goto('/ui/#/p/default/kardinal-test-app?compare=kardinal-test-app-prev111')
    const dialog = page.getByRole('dialog', { name: /comparison/i })
    const changed = dialog.locator('.bundle-diff__row--field[data-changed]').first()
    await expect(changed).toBeVisible()
    const [bg, wash] = await changed.evaluate((el) => [getComputedStyle(el).backgroundColor,
      getComputedStyle(document.documentElement).getPropertyValue('--k-status-blocked-wash').trim()])
    expect(bg).not.toBe(wash)
    for (const link of await dialog.locator('.bundle-diff__value--link').all()) {
      expect(await link.evaluate((a) => a.getBoundingClientRect().right <= a.parentElement!.getBoundingClientRect().right + 1)).toBe(true)
    }
  })
})
