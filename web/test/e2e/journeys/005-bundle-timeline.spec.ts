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

  test('Step 1: Bundle timeline shows bundle history header, and the History tab lists them', async ({ page }) => {
    await expect(page.getByText(/Versions \(oldest → newest\)/)).toBeVisible()
    await page.getByRole('tab', { name: /^Versions/ }).click()
    await expect(page.getByRole('list', { name: 'Versions' }).getByRole('listitem')).toHaveCount(3)
  })

  test('Step 2: Bundle timeline shows bundle phase', async ({ page }) => {
    // The fixture has a Promoting bundle
    await expect(page.getByText(/Promoting/i).first()).toBeVisible()
  })

  test('Step 3: the version rail reads oldest → newest (a time axis, newest on the right)', async ({ page }) => {
    await expect(page.getByText(/oldest → newest/i)).toBeVisible()
  })

  test('Step 4: Previous bundle (Superseded) shows its display label, not a truncated value', async ({ page }) => {
    await expect(page.locator('.bundle-chip__phase', { hasText: 'Replaced by newer version' })).toBeVisible()
    await expect(page.getByText('Sup', { exact: true })).toHaveCount(0)
  })

  test('Step 5: Clicking an older bundle shows its graph and keeps it after the next poll', async ({ page }) => {
    // The timeline chips carry data-bundle-phase; the header's copy button also names the bundle.
    const current = page.locator('button[data-bundle-phase]', { hasText: 'abc123' })
    const older = page.locator('button[data-bundle-phase]', { hasText: 'sha-9f8e7d6' })
    await expect(current).toHaveAttribute('aria-pressed', 'true')
    // The station map labels each stage: under abc123 a gate holds prod (its PR is open).
    await expect(page.getByRole('button', { name: /^prod — Held by gate no-weekend-deploys/ })).toBeVisible()

    await older.click()
    await expect(older).toHaveAttribute('aria-pressed', 'true')
    await expect(current).toHaveAttribute('aria-pressed', 'false')
    // The mock has no graph for prev111, so abc123's states go away.
    await expect(page.getByRole('button', { name: /^prod — Held by gate no-weekend-deploys/ })).toHaveCount(0)

    // Wait past one 5 s poll of the bundle list.
    await page.waitForResponse(res => res.url().includes('/pipelines/kardinal-test-app/bundles'), { timeout: 8_000 })
    await page.waitForTimeout(300)
    await expect(older).toHaveAttribute('aria-pressed', 'true')
  })
})
