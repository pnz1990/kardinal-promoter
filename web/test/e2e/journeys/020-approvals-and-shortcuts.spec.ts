// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 020 (U7): the approvals inbox, role-aware controls and the keyboard.
// The top bar counts the approvals waiting on you and opens #/approvals, which
// lists them oldest first with Approve and Decline through the drawer's
// dialog. A caller without the role sees the actions in place but inert, with
// the role that grants them. j/k walk the environments, a approves, s ships,
// and ? lists them. The mock server picks the caller from the bearer token:
// none is carol (an approver), "viewer" is victor (read only).

import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

async function axe(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target).join(' ')}`)).toEqual([])
}

async function asViewer(page: Page) {
  await page.addInitScript(() => sessionStorage.setItem('kardinal-ui-token', 'viewer'))
}

test.describe('Journey 020 — Approvals, access and shortcuts', () => {
  test('the top bar counts what waits on me; the inbox approves through the same dialog', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/')
    const link = page.getByRole('link', { name: 'Approvals, 1 waiting on you' })
    await link.click()
    await expect(page).toHaveURL(/#\/approvals$/)
    await expect(page.getByRole('heading', { level: 1, name: '1 waiting on you' })).toBeVisible()
    const row = page.getByRole('list', { name: 'Waiting on you' }).getByRole('listitem')
    await expect(row).toContainText('payments-service › prod')
    await expect(row).toContainText('6.14.1')
    await expect(row).toContainText('release-approval')
    await expect(row.getByRole('img', { name: '1 of 2 approvals' })).toBeVisible()
    await axe(page)

    await row.getByRole('button', { name: 'Approve' }).click()
    const dialog = page.getByRole('dialog', { name: 'Approve 6.14.1 for prod?' })
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Approve' }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Approved.' })).toContainText('Approved. Recorded: carol approves payments-service-def456 for prod')
    await expect(row.getByRole('button', { name: 'Approve' })).toBeFocused()

    // The row's place opens that environment's drawer at why it's waiting.
    await row.getByRole('link', { name: 'payments-service, prod' }).click()
    await expect(page).toHaveURL(/#\/p\/default\/payments-service\/env\/prod\?section=why$/)
    await expect(page.getByTestId('stage-drawer')).toBeVisible()
  })

  test('a viewer sees the actions in place but inert, with the role that grants them', async ({ page }) => {
    await asViewer(page)
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/ui/#/p/default/payments-service/env/prod')
    const drawer = page.getByTestId('stage-drawer')
    const approve = drawer.locator('[data-section="why"] [data-action="approve"]')
    await expect(approve).toHaveAttribute('aria-disabled', 'true')
    await expect(approve).toHaveAttribute('title', 'Approving needs the approver or admin role.')
    await expect(drawer.getByRole('list', { name: 'Not available to you' })).toContainText('Pausing the pipeline needs the promoter or admin role.')
    // Playwright waits for an aria-disabled button to be enabled; force the click to show it does nothing.
    await approve.click({ force: true })
    await expect(page.getByRole('dialog', { name: /^Approve/ })).toHaveCount(0)
    const ship = page.getByRole('button', { name: 'Ship a version of payments-service' })
    await expect(ship).toHaveAttribute('aria-disabled', 'true')
    await page.keyboard.press('s')
    await expect(page.getByRole('status').filter({ hasText: 'Shipping a version' })).toHaveText('Shipping a version needs the promoter or admin role.')
    await axe(page)
  })

  test('j/k walk the environments, a approves, s ships, ? lists them', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/ui/#/p/default/payments-service')
    await expect(page.getByRole('heading', { level: 1, name: 'payments-service' })).toBeVisible()
    // The keys act on what is loaded: wait for the graph.
    await expect(page.locator('svg.dag-view__svg')).toBeVisible()
    await page.keyboard.press('j')
    await expect(page).toHaveURL(/\/env\/staging$/)
    await page.keyboard.press('j')
    await expect(page).toHaveURL(/\/env\/prod$/)
    await page.keyboard.press('j')
    await expect(page.getByRole('status').filter({ hasText: 'last environment' })).toHaveText('prod is the last environment.')
    await page.keyboard.press('a')
    const approve = page.getByRole('dialog', { name: 'Approve 6.14.1 for prod?' })
    await expect(approve).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(approve).toBeHidden()
    await page.keyboard.press('k')
    await expect(page).toHaveURL(/\/env\/staging$/)

    await page.keyboard.press('s')
    const ship = page.getByRole('dialog', { name: /Create Bundle/ })
    await expect(ship).toBeVisible()
    await ship.getByRole('button', { name: 'Cancel bundle creation' }).click()
    await expect(page.getByRole('button', { name: 'Ship a version of payments-service' })).toBeFocused()

    await page.keyboard.press('?')
    const help = page.getByRole('dialog', { name: 'Keyboard shortcuts' })
    for (const k of ['j / k', 'a', 's']) await expect(help.getByText(k, { exact: true })).toBeVisible()
    await axe(page)
    // The top bar's ? opens the same list, for a mouse or a touch screen.
    await page.keyboard.press('Escape')
    await expect(help).toHaveCount(0)
    await page.getByRole('button', { name: 'Keyboard shortcuts' }).click()
    await expect(help).toBeVisible()
  })

  test('the inbox in dark and at 390 px: axe passes, nothing overflows', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'dark' })
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto('/ui/#/approvals')
    await expect(page.getByRole('heading', { level: 1, name: '1 waiting on you' })).toBeVisible()
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'))
    await axe(page)
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)
    expect(overflow).toBe(false)
  })
})
