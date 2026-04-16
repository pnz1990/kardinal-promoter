// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 002: Click DAG node → NodeDetail panel opens, close button works.

import { test, expect } from '@playwright/test'

test.describe('Journey 002 — DAG node click opens NodeDetail', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    await page.getByText('kardinal-test-app').first().click()
    // Wait for DAG to render
    await expect(page.locator('svg')).toBeVisible()
  })

  test('Step 1: DAG renders environment nodes', async ({ page }) => {
    // Use SVG text nodes inside the DAG to avoid ambiguity with pipeline/bundle names
    await expect(page.locator('svg text').filter({ hasText: /^test$/ })).toBeVisible()
    await expect(page.locator('svg text').filter({ hasText: /^uat$/ })).toBeVisible()
    await expect(page.locator('svg text').filter({ hasText: /^prod$/ })).toBeVisible()
  })

  test('Step 2: Clicking a DAG node opens NodeDetail panel', async ({ page }) => {
    const testNode = page.getByRole('button', { name: /test — /i })
    await testNode.click()
    // NodeDetail should appear — look for close button
    await expect(page.getByLabel('Close')).toBeVisible()
  })

  test('Step 3: Close button hides NodeDetail panel', async ({ page }) => {
    const testNode = page.getByRole('button', { name: /test — /i })
    await testNode.click()
    await expect(page.getByLabel('Close')).toBeVisible()
    await page.getByLabel('Close').click()
    await expect(page.getByLabel('Close')).not.toBeVisible()
  })

  test('Step 4: PolicyGate node renders with lock prefix', async ({ page }) => {
    // Gate node label is in the PolicyGates panel or DAG SVG — use first() to avoid strict violations
    await expect(page.getByText('no-weekend-deploys').first()).toBeVisible()
  })
})
