// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Journey 017 — a phone-width screen (390 px): below the md breakpoint the pipeline
// list is a drawer opened from the top bar, and the main panel takes the full width.
import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

test.use({ viewport: { width: 390, height: 844 } })

test.describe('Journey 017 — Narrow screen', () => {
  test('the main panel gets at least 90% of the width, and the list opens as a drawer', async ({ page }) => {
    await page.goto('/ui/#/p/default/kardinal-test-app')
    const main = page.locator('#main-content')
    await expect(page.getByRole('heading', { level: 1, name: 'kardinal-test-app' })).toBeVisible()
    const box = await main.boundingBox()
    expect(box!.width).toBeGreaterThanOrEqual(390 * 0.9)
    await expect(page.locator('#app-sidebar')).toBeHidden()

    const open = page.getByRole('button', { name: 'Pipelines', exact: true })
    await expect(open).toHaveAttribute('aria-expanded', 'false')
    await open.click()
    const drawer = page.getByRole('dialog', { name: 'Pipelines' })
    await expect(drawer).toBeVisible()
    await expect(open).toHaveAttribute('aria-expanded', 'true')
    expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze()).violations.map(v => v.id)).toEqual([])

    // Esc closes it and gives focus back to the button.
    await page.keyboard.press('Escape')
    await expect(drawer).toBeHidden()
    await expect(open).toBeFocused()

    // Picking a pipeline goes there and closes the drawer.
    await open.click()
    await drawer.getByRole('list', { name: 'Pipelines' }).getByText('payments-service').first().click()
    await expect(page.getByRole('heading', { level: 1, name: 'payments-service' })).toBeVisible()
    await expect(drawer).toBeHidden()
  })

  test('the fleet board also gets the full width', async ({ page }) => {
    await page.goto('/ui/#/')
    await expect(page.getByRole('region', { name: 'Fleet' })).toBeVisible()
    expect((await page.locator('#main-content').boundingBox())!.width).toBeGreaterThanOrEqual(390 * 0.9)
  })
})
