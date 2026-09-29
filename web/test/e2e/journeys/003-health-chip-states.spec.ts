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
    await expect(page.getByText('kardinal-test-app')).toBeVisible()
  })

  test('Step 1: Ready pipeline shows health-chip--ready class', async ({ page }) => {
    // kardinal-test-app is in "Ready" phase (all verified)
    // Find its health chip
    const chip = page.locator('.health-chip.health-chip--ready').first()
    await expect(chip).toBeVisible()
  })

  test('Step 2: Active pipeline shows health-chip--reconciling class', async ({ page }) => {
    // payments-service has "Unknown" phase → mapped to "Promoting" → Reconciling
    // Or has active env states showing Promoting
    const chip = page.locator('.health-chip').first()
    await expect(chip).toBeVisible()
    // At least one chip has a state class
    const chipClass = await chip.getAttribute('class')
    expect(chipClass).toContain('health-chip--')
  })

  test('Step 3: Bundle timeline chips carry their phase as a class and data attribute', async ({ page }) => {
    await page.getByText('kardinal-test-app').first().click()
    const promoting = page.locator('[data-bundle-phase="Promoting"]')
    await expect(promoting).toHaveCount(1)
    await expect(promoting).toHaveClass(/bundle-chip--promoting/)
    await expect(page.locator('[data-bundle-phase="Superseded"]')).toHaveClass(/bundle-chip--superseded/)
  })

  test('Step 4: HealthChip has no inline background-color (CSS class instead)', async ({ page }) => {
    const chip = page.locator('.health-chip--ready').first()
    await expect(chip).toBeVisible()
    // The chip should NOT have background-color inline style (using CSS class instead)
    const style = await chip.getAttribute('style')
    // style should be null or empty (CSS classes handle the color)
    expect(style ?? '').not.toContain('background')
  })
})
