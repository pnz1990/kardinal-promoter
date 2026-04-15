// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 004: Blocked gates auto-expand — regression guard for #524.

import { test, expect } from '@playwright/test'

test.describe('Journey 004 — Policy gates auto-expand when blocked (#524)', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    // Wait for React hydration and first API poll
    await page.waitForLoadState('networkidle')
    // Select the pipeline that has blocked gates (kardinal-test-app)
    await page.getByText('kardinal-test-app').first().click()
    await page.waitForTimeout(800) // wait for data load
  })

  test('Step 1: Policy Gates section is visible', async ({ page }) => {
    await expect(page.getByText(/Policy Gates/i)).toBeVisible()
  })

  test('Step 2: Policy Gates section is auto-expanded (blocked gate present)', async ({ page }) => {
    // The panel button should be aria-expanded=true because no-weekend-deploys is blocked
    const toggleBtn = page.getByRole('button', { name: /Policy Gates/i })
    await expect(toggleBtn).toHaveAttribute('aria-expanded', 'true')
  })

  test('Step 3: Blocked gate CEL expression is visible', async ({ page }) => {
    // Gate is expanded, so the expression should be visible
    await expect(page.getByText('!schedule.isWeekend()')).toBeVisible()
  })

  test('Step 4: Blocked gate summary shows "blocked" count', async ({ page }) => {
    await expect(page.getByText(/blocked/i)).toBeVisible()
  })

  test('Step 5: Toggle button collapses the panel', async ({ page }) => {
    const toggleBtn = page.getByRole('button', { name: /Policy Gates/i })
    await toggleBtn.click()
    await expect(toggleBtn).toHaveAttribute('aria-expanded', 'false')
  })
})
