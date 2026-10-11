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
    await expect(page.getByTestId('stage-drawer')).toContainText('prod')
    await page.reload()
    await expect(page.getByTestId('stage-drawer')).toContainText('prod')

    // Back closes the details, Forward opens them again.
    await page.getByTestId('stage-drawer').getByRole('button', { name: 'Close' }).click()
    await expect(page).toHaveURL(/#\/p\/default\/kardinal-test-app$/)
    await page.goBack()
    await expect(page.getByTestId('stage-drawer')).toBeVisible()
    await page.goForward()
    await expect(page.getByTestId('stage-drawer')).toHaveCount(0)

    // A version from the history is its own route.
    await page.getByRole('toolbar', { name: /Bundle history/ }).locator('.bundle-chip').nth(1).click()
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
    await expect(page.getByTestId('stage-drawer')).toContainText('uat')

    // The visible button opens it too, and Esc closes it.
    await page.getByRole('button', { name: /^Go to or do/ }).click()
    await expect(palette).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(palette).toHaveCount(0)
  })
})
