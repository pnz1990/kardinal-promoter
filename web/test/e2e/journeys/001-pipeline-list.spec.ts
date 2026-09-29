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

  test('Step 6: The logo loads from the /ui/ base path', async ({ page }) => {
    // The controller serves static files only under /ui/ (E2E-10); the mock does the same.
    const logo = page.getByRole('img', { name: 'Kardinal' })
    await expect(logo).toBeVisible()
    await expect(logo).toHaveAttribute('src', '/ui/logo.png')
    await expect.poll(() => logo.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true)
  })
})

// C10b-web-09: the mock sends the controller's security headers, so this
// catches any change that the UI's Content-Security-Policy would block.
test.describe('Journey 001 — UI security headers', () => {
  test('The UI refuses framing and runs with no CSP violations', async ({ page }) => {
    await page.addInitScript(() => {
      const seen: string[] = []
      ;(window as unknown as { cspViolations: string[] }).cspViolations = seen
      document.addEventListener('securitypolicyviolation', e => {
        seen.push(`${e.violatedDirective} ${e.blockedURI}`)
      })
    })
    const resp = await page.goto('/ui/')
    expect(resp?.headers()['x-frame-options']).toBe('DENY')
    expect(resp?.headers()['content-security-policy']).toContain("frame-ancestors 'none'")

    await page.getByText('kardinal-test-app').first().click()
    await page.getByRole('button', { name: /test — /i }).click()
    await expect(page.locator('[data-testid="node-detail"]').first()).toBeVisible()

    const violations = await page.evaluate(() => (window as unknown as { cspViolations: string[] }).cspViolations)
    expect(violations).toEqual([])
  })
})
