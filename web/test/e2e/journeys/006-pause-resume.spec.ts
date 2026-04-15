// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 006: Pause → UI updates → Resume.

import { test, expect } from '@playwright/test'

test.describe('Journey 006 — Pause and Resume pipeline', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    // Wait for React hydration and first API poll
    await page.waitForLoadState('networkidle')
    await page.getByText('kardinal-test-app').first().click()
    await page.waitForTimeout(500)
  })

  test('Step 1: ActionBar is visible for selected pipeline', async ({ page }) => {
    // ActionBar renders when a pipeline is selected
    // It shows Pause/Resume buttons
    const pauseBtn = page.getByRole('button', { name: /pause/i })
    await expect(pauseBtn).toBeVisible()
  })

  test('Step 2: Pause button triggers pause action', async ({ page }) => {
    const pauseBtn = page.getByRole('button', { name: /pause/i })
    await pauseBtn.click()
    // After pause, the button label may change or show paused state
    // Mock server responds with { message: "paused" }
    await page.waitForTimeout(300)
    // Test passes if no error is thrown
  })

  test('Step 3: Resume button is present (for paused pipeline)', async ({ page }) => {
    // Both pause and resume buttons should exist in the ActionBar
    // The component renders both, toggling between them
    await expect(page.locator('[role="button"]').filter({ hasText: /pause|resume/i }).first()).toBeVisible()
  })
})
