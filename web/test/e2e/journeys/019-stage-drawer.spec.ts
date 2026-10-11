// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 019 (U3): the stage drawer. From the home page a blocked prod is
// two clicks from Approve: its station opens the drawer, "why it's waiting"
// comes first and names the approval gate with its message and age, and its
// Approve asks before it records. Esc closes the dialog, then the drawer;
// the URL carries its environment (#/p/<ns>/<pipeline>/env/<env>), so Back and a reload reopen it. WCAG 2.1 AA
// holds in both themes with the drawer and its dialog open.

import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

async function axe(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target).join(' ')}`)).toEqual([])
}

test.describe('Journey 019 — Stage drawer', () => {
  test('approve on a blocked prod is two clicks from home', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/')
    let clicks = 0
    // 1: the prod station of the pipeline whose prod an approval gate holds.
    await page.getByRole('button', { name: /^payments-service prod:/ }).click(); clicks++
    const drawer = page.getByTestId('stage-drawer')
    await expect(drawer).toBeVisible()
    await expect(page).toHaveURL(/\/env\/prod/)
    // At lg and wider the drawer sits beside the page: the page gives up its column.
    const reserved = await page.evaluate(() => {
      const main = document.getElementById('main-content') as HTMLElement
      const d = document.querySelector('[data-testid="stage-drawer"]') as HTMLElement
      return { pad: parseFloat(getComputedStyle(main).paddingRight), drawer: d.getBoundingClientRect().width,
        mainEnd: main.getBoundingClientRect().right - parseFloat(getComputedStyle(main).paddingRight), drawerStart: d.getBoundingClientRect().left }
    })
    expect(Math.abs(reserved.pad - reserved.drawer)).toBeLessThan(2)
    expect(reserved.mainEnd).toBeLessThanOrEqual(reserved.drawerStart + 1)
    const why = drawer.locator('[data-section="why"]')
    await expect(why.getByRole('heading', { name: 'Why it’s waiting' })).toBeVisible()
    await expect(why).toContainText('Needs 1 more approval (1 of 2) in release-approval.')
    await expect(drawer).toContainText('Waiting for approval in release-approval')
    await expect(drawer).toContainText('6.14.1')
    await expect(why).toContainText('waiting for approvals: 1 of 2 (alice)')
    await expect(why).toContainText(/evaluated \d+s ago/)
    // 2: Approve, next to its reason (offered once).
    await expect(drawer.locator('[data-action="approve"]')).toHaveCount(1)
    await why.locator('[data-action="approve"]').click(); clicks++
    const dialog = page.getByRole('dialog', { name: 'Approve 6.14.1 for prod?' })
    await expect(dialog).toBeVisible()
    expect(clicks).toBeLessThanOrEqual(2)
    await axe(page)

    await dialog.getByRole('button', { name: 'Approve' }).click()
    await expect(drawer.getByRole('status')).toContainText('Approved. Recorded: carol approves payments-service-def456 for prod')
  })

  test('Esc closes the dialog first, then the drawer; Back reopens it', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/')
    await page.getByRole('button', { name: /^payments-service prod:/ }).click()
    const drawer = page.getByTestId('stage-drawer')
    await drawer.locator('[data-action="reject"]').click()
    const dialog = page.getByRole('dialog', { name: /^Decline 6\.14\.1 for prod\?$/ })
    await expect(dialog).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(dialog).toBeHidden()
    await expect(drawer).toBeVisible()
    await expect(drawer.locator('[data-action="reject"]')).toBeFocused()
    await page.keyboard.press('Escape')
    await expect(drawer).toBeHidden()
    await expect(page).not.toHaveURL(/\/env\//)
    await page.goBack()
    await expect(page.getByTestId('stage-drawer')).toBeVisible()
    await page.reload()
    await expect(page.getByTestId('stage-drawer')).toBeVisible()
  })

  test('raw CEL stays behind Details; dark theme passes axe; a narrow screen gets a modal sheet', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/')
    await page.getByRole('button', { name: /^payments-service prod:/ }).click()
    const drawer = page.getByTestId('stage-drawer')
    const details = drawer.locator('[data-section="why"] details').first()
    await expect(details).not.toHaveAttribute('open', '')
    await page.emulateMedia({ colorScheme: 'dark' })
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'))
    await axe(page)

    await page.setViewportSize({ width: 390, height: 844 })
    await expect(page.getByRole('dialog', { name: 'prod' })).toBeVisible()
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)
    expect(overflow).toBe(false)
  })
})
