// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 008: Loading state clears after first fetch — regression guard for #522.
// Verifies that "● Loading..." is NOT persistent after data loads.

import { test, expect } from '@playwright/test'

test.describe('Journey 008 — Loading state clears (#522 regression guard)', () => {
  test('Step 1: Page loads without perpetual Loading... indicator', async ({ page }) => {
    await page.goto('/')
    // Wait for React hydration and first API poll
    await page.waitForLoadState('networkidle')

    // Wait up to 5s for the loading indicator to clear
    // After first successful fetch, "Loading..." should be replaced with "just now"
    await expect(page.getByText('Loading...')).not.toBeVisible({ timeout: 5000 })
  })

  test('Step 2: Freshness indicator shows "just now" after initial load', async ({ page }) => {
    await page.goto('/')
    // After data loads, the indicator should say "just now" or "Xs ago"
    await expect(page.getByText(/just now|ago/i)).toBeVisible({ timeout: 5000 })
  })

  test('Step 3: No dual "Loading..." + "just now" simultaneous render', async ({ page }) => {
    await page.goto('/')
    await page.waitForTimeout(1000) // let data load

    // Should never see both "Loading..." AND a timestamp at the same time
    const loadingCount = await page.getByText('Loading...').count()
    const freshnessCount = await page.getByText(/just now|ago/i).count()

    // If freshness is showing, loading should be gone
    if (freshnessCount > 0) {
      expect(loadingCount).toBe(0)
    }
  })

  test('Step 4: After pipeline selection, DAG loads without persistent spinner', async ({ page }) => {
    await page.goto('/')
    await page.getByText('kardinal-test-app').first().click()

    // DAG should render within 3s
    await expect(page.locator('svg')).toBeVisible({ timeout: 3000 })
    // Loading indicator should clear
    await expect(page.getByText('Loading...')).not.toBeVisible({ timeout: 5000 })
  })
})
