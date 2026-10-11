// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 011: Rollback button — visible in the stage drawer (U3) after clicking
// a PromotionStep; it asks for confirmation before calling the API.
//
// Design ref: docs/design/25-anchor-kardinal-promoter.md §Future
//   "Playwright integration in PDCA — first 3 UI scenarios: rollback button"

import { test, expect } from '@playwright/test'

test.describe('Journey 011 — Rollback button in the stage drawer', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    await page.getByText('kardinal-test-app').first().click()
    // Wait for DAG to render
    await expect(page.locator('svg.dag-view__svg')).toBeVisible()
  })

  test('Step 1: Clicking a PromotionStep node opens the stage drawer', async ({ page }) => {
    // The test node button is labelled "test — <state>" in the DAG
    const testNode = page.getByRole('button', { name: /test — /i })
    await testNode.click()
    // The stage drawer opens — check for its Close button
    await expect(page.getByLabel('Close test')).toBeVisible()
  })

  test('Step 2: Rollback button is visible in the stage drawer', async ({ page }) => {
    const testNode = page.getByRole('button', { name: /test — /i })
    await testNode.click()
    await expect(page.getByLabel('Close test')).toBeVisible()
    const nodeDetail = page.getByTestId('stage-drawer')
    const rollbackBtn = nodeDetail.locator('[data-action="rollback"]')
    await expect(rollbackBtn).toBeVisible()
  })

  test('Step 3: Rollback asks first, then calls the rollback API on confirm', async ({ page }) => {
    const testNode = page.getByRole('button', { name: /test — /i })
    await testNode.click()
    await expect(page.getByLabel('Close test')).toBeVisible()

    let rollbackCalls = 0
    page.on('request', req => {
      if (req.url().includes('/api/v1/ui/rollback') && req.method() === 'POST') rollbackCalls++
    })

    const nodeDetail = page.getByTestId('stage-drawer')
    await nodeDetail.locator('[data-action="rollback"]').click()

    // A confirmation dialog opens; nothing has been sent yet.
    const dialog = page.getByRole('dialog', { name: /^Roll test back/ })
    await expect(dialog).toBeVisible()
    expect(rollbackCalls).toBe(0)

    const rollbackRequest = page.waitForRequest(req =>
      req.url().includes('/api/v1/ui/rollback') && req.method() === 'POST'
    )
    await dialog.getByRole('button', { name: 'Roll back' }).click()
    const req = await rollbackRequest
    expect(req.postDataJSON()).toMatchObject({ pipeline: 'kardinal-test-app', environment: 'test' })

    await expect(dialog).toBeHidden()
    await expect(nodeDetail.getByRole('status')).toHaveText(/^Rolled back\. rollback started/)
  })

  test('Step 4: Cancel closes the dialog without calling the API', async ({ page }) => {
    await page.getByRole('button', { name: /test — /i }).click()
    let rollbackCalls = 0
    page.on('request', req => {
      if (req.url().includes('/api/v1/ui/rollback') && req.method() === 'POST') rollbackCalls++
    })
    const nodeDetail = page.getByTestId('stage-drawer')
    await nodeDetail.locator('[data-action="rollback"]').click()
    const dialog = page.getByRole('dialog', { name: /^Roll test back/ })
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toBeHidden()
    expect(rollbackCalls).toBe(0)
  })
})
