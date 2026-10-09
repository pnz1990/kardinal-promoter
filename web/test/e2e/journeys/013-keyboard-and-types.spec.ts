// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 013: keyboard-only use and Bundle type badges. The first Tab stop
// skips to the main content; the fleet board and the Bundle history are one
// Tab stop each, moved through with arrow keys; config and mixed Bundles
// carry a type badge. WCAG 2.1 AA (axe) holds in the light theme, with the
// node details open and with a dialog open.

import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

async function expectNoViolations(page: Page, what: string) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  const summary = results.violations.map(v => `${v.id}: ${v.nodes.slice(0, 2).map(n => n.target.join(' ')).join(', ')}`)
  expect(summary, what).toEqual([])
}

test.describe('Journey 013 — Keyboard and Bundle types', () => {
  test('the skip link is the first Tab stop and moves to the main content', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('region', { name: 'Fleet' })).toBeVisible()
    await page.keyboard.press('Tab')
    const skip = page.getByRole('link', { name: 'Skip to main content' })
    await expect(skip).toBeFocused()
    await expect(skip).toBeInViewport()
    await page.keyboard.press('Enter')
    await expect(page.locator('#main-content')).toBeFocused()
  })

  test('the fleet board is driven with the arrow keys', async ({ page }) => {
    await page.goto('/')
    const board = page.getByRole('region', { name: 'Fleet' })
    await board.getByRole('button', { name: 'kardinal-test-app', exact: true }).focus()
    await page.keyboard.press('Tab')
    await expect(board.getByRole('button', { name: /^kardinal-test-app test:/ })).toBeFocused()
    await page.keyboard.press('ArrowRight')
    await expect(board.getByRole('button', { name: /^kardinal-test-app uat:/ })).toBeFocused()
    await page.keyboard.press('ArrowDown')
    await expect(board.getByRole('button', { name: /^payments-service prod:/ })).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(page.getByRole('heading', { level: 1, name: 'payments-service' })).toBeVisible()
  })

  test('the Bundle history is one Tab stop with type badges', async ({ page }) => {
    await page.goto('/')
    await page.locator('aside').getByText('kardinal-test-app').first().click()
    const bar = page.getByRole('toolbar', { name: /Bundle history/ })
    await expect(bar).toBeVisible()
    await expect(bar.getByRole('img', { name: 'config Bundle' })).toHaveText('config')
    await expect(bar.getByRole('img', { name: 'image Bundle' })).toHaveCount(0)
    const chips = bar.locator('.bundle-chip')
    await expect(chips).toHaveCount(3)
    await expect(bar.locator('.bundle-chip[tabindex="0"]')).toHaveCount(1)
    await bar.locator('.bundle-chip[tabindex="0"]').focus()
    await page.keyboard.press('End')
    await expect(chips.last()).toBeFocused()
    await page.keyboard.press('Home')
    await expect(chips.first()).toBeFocused()
    // The Bundle card names the type of the shown Bundle.
    await expect(page.getByRole('img', { name: 'image Bundle' })).toBeVisible()

    await page.locator('aside').getByText('payments-service').first().click()
    await expect(page.getByRole('img', { name: 'image and config Bundle' }).first()).toHaveText('image + config')
  })

  test('WCAG 2.1 AA in the light theme, with node details and a dialog open', async ({ page }) => {
    await page.goto('/')
    await page.evaluate(() => localStorage.setItem('kardinal-theme', 'light'))
    await page.goto('/')
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
    await expect(page.getByRole('region', { name: 'Fleet' })).toBeVisible()
    await expectNoViolations(page, 'light fleet board')

    await page.locator('aside').getByText('kardinal-test-app').first().click()
    await expect(page.getByRole('toolbar', { name: /Bundle history/ })).toBeVisible()
    await expectNoViolations(page, 'light pipeline view')

    await page.locator('[role=button][tabindex="0"]').filter({ hasText: 'prod' }).first().click()
    await expect(page.getByTestId('node-detail')).toBeVisible()
    // The hover tooltip fades in over 80 ms; axe would measure it half
    // transparent. Move the pointer off the graph first.
    await page.mouse.move(0, 0)
    await expect(page.getByTestId('dag-tooltip')).toHaveCount(0)
    await expectNoViolations(page, 'light node details')

    await page.keyboard.press('Escape')
    await page.getByRole('button', { name: 'Create bundle for kardinal-test-app' }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
    await expectNoViolations(page, 'light create bundle dialog')
  })
})
