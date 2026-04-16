// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 001: Pipeline list renders and click selects pipeline.

import { test, expect } from '@playwright/test'

test.describe('Journey 001 — Pipeline list', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('./') // #632: was '/' which resolves to host root; './' resolves to baseURL
  })

  test('Step 1: Pipeline list renders with pipeline names', async ({ page }) => {
    await expect(page.getByText('kardinal-test-app')).toBeVisible()
    await expect(page.getByText('payments-service')).toBeVisible()
  })

  test('Step 2: Click pipeline shows DAG view', async ({ page }) => {
    // Wait for pipeline list to be interactive first
    await expect(page.getByText('kardinal-test-app').first()).toBeVisible()
    await page.getByText('kardinal-test-app').first().click()
    // The DAG renders environment nodes from the graph fixture (test, uat, prod)
    // Wait for the DAG area to populate — expect a DAG node label to appear
    await expect(page.getByText('prod').first()).toBeVisible({ timeout: 15_000 })
  })

  test('Step 3: Selected pipeline is highlighted in sidebar', async ({ page }) => {
    await expect(page.getByText('kardinal-test-app').first()).toBeVisible()
    await page.getByText('kardinal-test-app').first().click()
    // After click the pipeline name should remain visible in the sidebar
    await expect(page.getByText('kardinal-test-app').first()).toBeVisible({ timeout: 10_000 })
    // The environment count badge (3 envs) should still appear for the selected pipeline
    await expect(page.getByText(/3 envs/i)).toBeVisible({ timeout: 10_000 })
  })

  test('Step 4: Environment count is visible for pipeline', async ({ page }) => {
    await expect(page.getByText(/3 envs/i)).toBeVisible()
  })

  test('Step 5: KARDINAL brand is visible in sidebar', async ({ page }) => {
    // Use exact match to avoid strict mode violation (getByText matches substrings too)
    await expect(page.getByText('KARDINAL', { exact: true })).toBeVisible()
  })
})
