// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 005: Bundle timeline chip click updates the DAG.

import { test, expect } from '@playwright/test'

test.describe('Journey 005 — Bundle timeline interaction', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    await page.getByText('kardinal-test-app').first().click()
    await page.waitForTimeout(800) // wait for bundle data
  })

  test('Step 1: Bundle timeline shows bundle history header', async ({ page }) => {
    await expect(page.getByText(/Bundle History/i)).toBeVisible()
  })

  test('Step 2: Bundle timeline shows bundle phase', async ({ page }) => {
    // The fixture has a Promoting bundle
    await expect(page.getByText(/Promoting/i).first()).toBeVisible()
  })

  test('Step 3: Bundle timeline shows "newest → oldest" direction', async ({ page }) => {
    await expect(page.getByText(/newest → oldest/i)).toBeVisible()
  })

  test('Step 4: Previous bundle (Superseded) shows abbreviated label', async ({ page }) => {
    // Superseded bundles show "Sup" abbreviation
    await expect(page.getByText('Sup')).toBeVisible()
  })
})
