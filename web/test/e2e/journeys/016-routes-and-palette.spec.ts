// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Journey 016 — Routes and the command palette (U1, UX-AUDIT.md §4.3): every screen is a
// #/… route that survives a reload, Back and Forward walk it, old links redirect, the
// breadcrumb leads back, and ⌘K / Ctrl+K goes anywhere.
import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

test.describe('Journey 016 — Routes and the command palette', () => {
  test('a pipeline, an environment and a version are routes that survive a reload', async ({ page }) => {
    await page.goto('/ui/#/p/default/kardinal-test-app/env/prod')
    await expect(page.getByRole('heading', { level: 1, name: 'kardinal-test-app' })).toBeVisible()
    await expect(page.getByTestId('node-detail')).toContainText('prod')
    await page.reload()
    await expect(page.getByTestId('node-detail')).toContainText('prod')

    // Back closes the details, Forward opens them again.
    await page.getByTestId('node-detail').getByRole('button', { name: 'Close' }).click()
    await expect(page).toHaveURL(/#\/p\/default\/kardinal-test-app$/)
    await page.goBack()
    await expect(page.getByTestId('node-detail')).toBeVisible()
    await page.goForward()
    await expect(page.getByTestId('node-detail')).toHaveCount(0)

    // A version from the history is its own route.
    await page.getByRole('toolbar', { name: /^Versions/ }).locator('.bundle-chip').nth(1).click()
    await expect(page).toHaveURL(/#\/p\/default\/kardinal-test-app\/v\/[\w-]+$/)
    const url = page.url()
    await page.reload()
    await expect(page).toHaveURL(url)
    await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('kardinal-test-app')

    // The breadcrumb leads back to the fleet.
    await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Pipelines' }).click()
    await expect(page.getByRole('region', { name: 'Fleet' })).toBeVisible()
  })

  test('an unknown pipeline or version says not found and links back, never showing another', async ({ page }) => {
    await page.goto('/ui/#/p/default/no-such-pipeline')
    await expect(page.getByRole('heading', { name: 'No pipeline named no-such-pipeline in default' })).toBeVisible()
    await page.getByRole('link', { name: 'Back to all pipelines' }).click()
    await expect(page.getByRole('region', { name: 'Fleet' })).toBeVisible()

    await page.goto('/ui/#/p/default/kardinal-test-app/v/no-such-version')
    await expect(page.getByRole('heading', { name: 'No version no-such-version in kardinal-test-app' })).toBeVisible()
    await expect(page.locator('svg.dag-view__svg')).toHaveCount(0)
    await page.getByRole('link', { name: 'Show the current version of kardinal-test-app' }).click()
    await expect(page).toHaveURL(/#\/p\/default\/kardinal-test-app$/)
    await expect(page.locator('svg.dag-view__svg')).toBeVisible()
  })

  test('the pipeline tabs are Map, Versions, Activity and Metrics, and an old tab link still opens', async ({ page }) => {
    await page.goto('/ui/#/p/default/kardinal-test-app')
    await expect(page.getByRole('tab')).toHaveText([/^Map/, /^Versions/, /^Activity/, /^Metrics/])
    await page.goto('/ui/#/p/default/kardinal-test-app?tab=history')
    await expect(page.getByRole('tab', { name: /^Versions/ })).toHaveAttribute('aria-selected', 'true')
    await expect(page.getByRole('list', { name: 'Versions' })).toBeVisible()
    await expect(new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze().then(r => r.violations.map(v => v.id))).resolves.toEqual([])
  })

  test("a pipeline's own problems show under its header with the command that fixes them", async ({ page }) => {
    await page.goto('/ui/#/p/default/payments-service')
    const warning = page.locator('.pipeline-conditions__notice[data-tone="warning"]')
    await expect(warning).toContainText('Label the git Secret before upgrading to v0.11')
    await expect(warning.locator('pre')).toHaveText('kubectl label secret github-token -n default kardinal.io/referenceable=true')
    await expect(warning.getByRole('button', { name: 'Copy shell' })).toBeVisible()
  })

  test('a long fix command wraps instead of widening the page', async ({ page }) => {
    // The message the PipelineReconciler writes for a missing git Secret (git_credential.go).
    const message = 'spec.git.secretRef is not set, so git has no token for https://github.com/shop/payments-config; create the Secret '
      + '(kubectl create secret generic git-token -n default --from-literal=token=<TOKEN> && kubectl label secret git-token -n default kardinal.io/referenceable=true) '
      + 'and set spec.git.secretRef.name: git-token'
    await page.route('**/api/v1/ui/pipelines', async (route) => {
      const list = await (await route.fetch()).json()
      for (const p of list) if (p.name === 'payments-service') p.conditions = [{ type: 'Ready', status: 'False', reason: 'GitCredentialMissing', message }]
      await route.fulfill({ json: list })
    })
    for (const width of [1024, 1440]) {
      await page.setViewportSize({ width, height: 800 })
      await page.goto('/ui/#/p/default/payments-service')
      const error = page.locator('.pipeline-conditions__notice[data-tone="failed"]')
      await expect(error.locator('pre')).toHaveText(/^kubectl create secret generic git-token/)
      const main = page.locator('#main-content')
      expect(await main.evaluate((m) => m.scrollWidth - m.clientWidth), `main scrolls sideways at ${width}`).toBeLessThanOrEqual(0)
    }
  })

  test('an old link redirects to its route', async ({ page }) => {
    await page.goto('/ui/#pipeline=kardinal-test-app&ns=default')
    await expect(page.getByRole('heading', { level: 1, name: 'kardinal-test-app' })).toBeVisible()
    await expect(page).toHaveURL(/#\/p\/default\/kardinal-test-app$/)
  })

  test('the view and the fleet filter are in the URL', async ({ page }) => {
    await page.goto('/ui/#/?view=ops')
    await expect(page.getByRole('button', { name: 'Switch to List View' })).toBeVisible()
    await page.reload()
    await expect(page.getByRole('button', { name: 'Switch to List View' })).toBeVisible()
    await page.getByRole('button', { name: 'Switch to List View' }).click()
    await expect(page).toHaveURL(/#\/$/)
  })

  test('Ctrl+K opens the palette; it is keyboard-operable, counts its results and passes axe', async ({ page }) => {
    await page.goto('/ui/#/')
    await expect(page.getByRole('region', { name: 'Fleet' })).toBeVisible()
    await page.keyboard.press('Control+k')
    const palette = page.getByRole('dialog', { name: 'Go to or do' })
    await expect(palette).toBeVisible()
    const input = palette.getByRole('combobox')
    await expect(input).toBeFocused()
    const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()
    expect(results.violations.map((v) => v.id)).toEqual([])

    await input.fill('kardinal-test-app › uat')
    await expect(palette.getByRole('status')).toContainText(/match/)
    await expect(palette.getByRole('option').first()).toHaveText(/kardinal-test-app › uat/)
    await page.keyboard.press('Enter')
    await expect(palette).toHaveCount(0)
    await expect(page).toHaveURL(/#\/p\/default\/kardinal-test-app\/env\/uat$/)
    await expect(page.getByTestId('node-detail')).toContainText('uat')

    // The visible button opens it too, and Esc closes it.
    await page.getByRole('button', { name: /^Go to or do/ }).click()
    await expect(palette).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(palette).toHaveCount(0)
  })
})
