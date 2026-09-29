// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 005: Bundle timeline chip click updates the DAG, and the choice
// survives the next poll (C10b-web-02).

import { test, expect } from '@playwright/test'

test.describe('Journey 005 — Bundle timeline interaction', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    await page.getByText('kardinal-test-app').first().click()
    await page.waitForTimeout(800) // wait for bundle data
  })

  test('Step 1: Bundle timeline shows bundle history header', async ({ page }) => {
    // Use the collapsible button which contains "Bundle history" text
    await expect(page.getByRole('button', { name: /Bundle history/i })).toBeVisible()
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

  test('Step 5: Clicking an older bundle shows its graph and keeps it after the next poll', async ({ page }) => {
    // The timeline chips carry data-bundle-phase; the header's copy button also names the bundle.
    const current = page.locator('button[data-bundle-phase]', { hasText: 'abc123' })
    const older = page.locator('button[data-bundle-phase]', { hasText: 'prev111' })
    await expect(current).toHaveAttribute('aria-pressed', 'true')
    await expect(page.getByRole('button', { name: 'prod — WaitingForMerge' })).toBeVisible()

    await older.click()
    await expect(older).toHaveAttribute('aria-pressed', 'true')
    await expect(current).toHaveAttribute('aria-pressed', 'false')
    // The mock has no graph for prev111, so the abc123 nodes go away.
    await expect(page.getByRole('button', { name: 'prod — WaitingForMerge' })).toHaveCount(0)

    // Wait past one 5 s poll of the bundle list.
    await page.waitForResponse(res => res.url().includes('/pipelines/kardinal-test-app/bundles'), { timeout: 8_000 })
    await page.waitForTimeout(300)
    await expect(older).toHaveAttribute('aria-pressed', 'true')
  })
})
