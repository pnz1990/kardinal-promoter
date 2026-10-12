// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 003: Health chip CSS classes per state — regression guard for #532.
// Verifies that state-driven visual properties use CSS classes, not inline hex.

import { test, expect } from '@playwright/test'

test.describe('Journey 003 — Health chip CSS class regression guard (#532)', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    // Wait for pipeline list to render
    await expect(page.getByRole('complementary').getByText('kardinal-test-app')).toBeVisible()
  })

  // The sidebar shows each pipeline's one state (pipelineState) as a StatusChip whose tone is a
  // class (k-chip--<tone>), never an inline colour.
  test('Step 1: a pipeline held by a gate shows the blocked tone class', async ({ page }) => {
    const chip = page.locator('.app-sidebar [data-category="blocked"]').first()
    await expect(chip).toBeVisible()
    await expect(chip).toHaveClass(/k-chip--blocked/)
  })

  test('Step 2: every sidebar state chip carries a tone class', async ({ page }) => {
    const chips = page.locator('.app-sidebar .k-chip')
    await expect(chips.first()).toBeVisible()
    for (const chip of await chips.all()) expect(await chip.getAttribute('class')).toMatch(/k-chip--\w+/)
  })

  test('Step 3: Bundle timeline chips carry their phase as a class and data attribute', async ({ page }) => {
    await page.getByText('kardinal-test-app').first().click()
    const promoting = page.locator('[data-bundle-phase="Promoting"]')
    await expect(promoting).toHaveCount(1)
    await expect(promoting).toHaveClass(/bundle-chip--promoting/)
    await expect(page.locator('[data-bundle-phase="Superseded"]')).toHaveClass(/bundle-chip--superseded/)
  })

  test('Step 4: the sidebar state chip has no inline background-color (CSS class instead)', async ({ page }) => {
    // The sidebar shows each pipeline's one state (pipelineState) as a StatusChip.
    const chip = page.locator('.app-sidebar .k-chip').first()
    await expect(chip).toBeVisible()
    // The chip should NOT have background-color inline style (using CSS class instead)
    const style = await chip.getAttribute('style')
    // style should be null or empty (CSS classes handle the color)
    expect(style ?? '').not.toContain('background')
  })
})
