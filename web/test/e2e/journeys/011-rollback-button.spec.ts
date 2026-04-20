// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 011: Rollback button — visible in NodeDetail after clicking a PromotionStep.
//
// Design ref: docs/design/25-anchor-kardinal-promoter.md §Future
//   "Playwright integration in PDCA — first 3 UI scenarios: rollback button"
//
// PDCA scenario: S27

import { test, expect } from '@playwright/test'

test.describe('Journey 011 — Rollback button in NodeDetail', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    await page.getByText('kardinal-test-app').first().click()
    // Wait for DAG to render
    await expect(page.locator('svg')).toBeVisible()
  })

  test('Step 1: Clicking a PromotionStep node opens NodeDetail', async ({ page }) => {
    // The test node button is labelled "test — <state>" in the DAG
    const testNode = page.getByRole('button', { name: /test — /i })
    await testNode.click()
    // NodeDetail panel opens — check for Close button
    await expect(page.getByLabel('Close')).toBeVisible()
  })

  test('Step 2: Rollback button is visible in NodeDetail', async ({ page }) => {
    const testNode = page.getByRole('button', { name: /test — /i })
    await testNode.click()
    await expect(page.getByLabel('Close')).toBeVisible()
    // Rollback button inside NodeDetail — scope to panel to avoid matching DAG node buttons
    const nodeDetail = page.locator('[data-testid="node-detail"], [aria-label*="Node detail"], .node-detail').first()
    const rollbackBtn = nodeDetail.getByRole('button', { name: /rollback/i }).first()
    await expect(rollbackBtn).toBeVisible()
  })

  test('Step 3: Rollback button click triggers rollback API call', async ({ page }) => {
    const testNode = page.getByRole('button', { name: /test — /i })
    await testNode.click()
    await expect(page.getByLabel('Close')).toBeVisible()

    // Intercept POST /api/v1/ui/rollback
    const rollbackRequest = page.waitForRequest(req =>
      req.url().includes('/api/v1/ui/rollback') && req.method() === 'POST'
    )

    // Scope to NodeDetail panel to avoid strict mode violation from multiple rollback buttons
    const nodeDetail = page.locator('[data-testid="node-detail"], [aria-label*="Node detail"], .node-detail').first()
    const rollbackBtn = nodeDetail.getByRole('button', { name: /rollback/i }).first()
    await rollbackBtn.click()

    // Wait for the API call — mock server responds with { bundle: 'rollback-bundle', message: 'rollback started' }
    const req = await rollbackRequest
    expect(req).toBeTruthy()

    // UI should update to show success state
    await expect(page.getByRole('button', { name: /rollback started!/i })).toBeVisible()
  })
})
