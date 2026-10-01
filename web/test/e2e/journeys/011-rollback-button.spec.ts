// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 011: Rollback button — visible in NodeDetail after clicking a PromotionStep;
// it asks for confirmation before calling the API.
//
// Design ref: docs/design/25-anchor-kardinal-promoter.md §Future
//   "Playwright integration in PDCA — first 3 UI scenarios: rollback button"

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
    // Design ref: docs/design/14-v060-roadmap.md §14.6 PDCA Playwright fix
    const testNode = page.getByRole('button', { name: /test — /i })
    await testNode.click()
    await expect(page.getByLabel('Close')).toBeVisible()
    const nodeDetail = page.locator('[data-testid="node-detail"]').first()
    const rollbackBtn = nodeDetail.getByRole('button', { name: /^Roll back test$/ })
    await expect(rollbackBtn).toBeVisible()
  })

  test('Step 3: Rollback asks first, then calls the rollback API on confirm', async ({ page }) => {
    // Design ref: docs/design/14-v060-roadmap.md §14.6 PDCA Playwright fix
    const testNode = page.getByRole('button', { name: /test — /i })
    await testNode.click()
    await expect(page.getByLabel('Close')).toBeVisible()

    let rollbackCalls = 0
    page.on('request', req => {
      if (req.url().includes('/api/v1/ui/rollback') && req.method() === 'POST') rollbackCalls++
    })

    const nodeDetail = page.locator('[data-testid="node-detail"]').first()
    await nodeDetail.getByRole('button', { name: /^Roll back test$/ }).click()

    // A confirmation dialog opens; nothing has been sent yet.
    const dialog = page.getByRole('dialog', { name: 'Roll back test?' })
    await expect(dialog).toBeVisible()
    expect(rollbackCalls).toBe(0)

    const rollbackRequest = page.waitForRequest(req =>
      req.url().includes('/api/v1/ui/rollback') && req.method() === 'POST'
    )
    await dialog.getByRole('button', { name: 'Roll back test' }).click()
    const req = await rollbackRequest
    expect(req.postDataJSON()).toMatchObject({ pipeline: 'kardinal-test-app', environment: 'test' })

    await expect(dialog).toBeHidden()
    await expect(nodeDetail.getByRole('status')).toHaveText(/Rollback started: bundle rollback-bundle/)
  })

  test('Step 4: Cancel closes the dialog without calling the API', async ({ page }) => {
    await page.getByRole('button', { name: /test — /i }).click()
    let rollbackCalls = 0
    page.on('request', req => {
      if (req.url().includes('/api/v1/ui/rollback') && req.method() === 'POST') rollbackCalls++
    })
    const nodeDetail = page.locator('[data-testid="node-detail"]').first()
    await nodeDetail.getByRole('button', { name: /^Roll back test$/ }).click()
    const dialog = page.getByRole('dialog', { name: 'Roll back test?' })
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toBeHidden()
    expect(rollbackCalls).toBe(0)
  })
})
