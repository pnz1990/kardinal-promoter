// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 001: Pipeline list renders and click selects pipeline.

import { test, expect } from '@playwright/test'

test.describe('Journey 001 — Pipeline list', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
  })

  test('Step 1: Pipeline list renders with pipeline names', async ({ page }) => {
    await expect(page.getByText('kardinal-test-app')).toBeVisible()
    await expect(page.getByText('payments-service')).toBeVisible()
  })

  test('Step 2: Click pipeline shows DAG view', async ({ page }) => {
    await page.getByText('kardinal-test-app').first().click()
    // DAG SVG should become visible
    await expect(page.locator('svg')).toBeVisible()
  })

  test('Step 3: Selected pipeline is highlighted in sidebar', async ({ page }) => {
    await page.getByText('kardinal-test-app').first().click()
    // The selected pipeline item uses aria-pressed=true (button pattern, #762).
    // aria-pressed on a <button> indicates the button is in a "pressed/selected" state.
    const selectedItem = page.locator('li [aria-pressed="true"]').first()
    await expect(selectedItem).toBeVisible()
  })

  test('Step 4: Environment count is visible for pipeline', async ({ page }) => {
    await expect(page.getByText(/3 envs/i)).toBeVisible()
  })

  test('Step 5: KARDINAL brand is visible in sidebar', async ({ page }) => {
    // Use exact match to avoid matching pipeline names that contain "kardinal"
    await expect(page.getByText('KARDINAL', { exact: true })).toBeVisible()
  })
})
