// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 023: without sign-in (no TokenReview) an approval from the UI cannot
// name who decided, so POST /access answers create approvals with
// allowed:false. Every surface that offers Approve (the drawer, the Table and
// the Activity tab) shows it inert, with one line under it that says why and
// gives the command that works, with a copy button: once per surface, not
// again in a list further down. The tooltips, the a key and ⌘K use the same
// words. The mock server's "open" token is a UI without TokenReview; no token
// is carol, an approver, for whom a and ⌘K open the approval.

import { test, expect, type Locator, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

const command = 'kardinal approve payments-service prod'
const note = `Approving needs sign-in with your Kubernetes identity — run ${command}`

async function axe(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target).join(' ')}`)).toEqual([])
}

/** Approve is in place, inert and does nothing; the one sign-in line names the command. */
async function honest(page: Page, surface: Locator, approve: Locator) {
  await expect(approve).toHaveAttribute('aria-disabled', 'true')
  const lines = surface.locator('.sign-in-note')
  await expect(lines).toHaveCount(1)
  await expect(lines).toHaveText(note)
  await expect(lines.getByRole('button', { name: `Copy ${command}` })).toBeVisible()
  // Playwright waits for an aria-disabled button to be enabled; force the click to show it does nothing.
  await approve.click({ force: true })
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByRole('status').filter({ hasText: /Recorded|Approved/ })).toHaveCount(0)
}

test.describe('Journey 023 — Approve without sign-in', () => {
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => sessionStorage.setItem('kardinal-ui-token', 'open'))
    await page.setViewportSize({ width: 1440, height: 900 })
  })

  test('the drawer: Approve is inert, one line under it, no second explanation below', async ({ page }) => {
    await page.goto('/ui/#/p/default/payments-service/env/prod')
    const drawer = page.getByTestId('stage-drawer')
    const why = drawer.locator('[data-section="why"]')
    await honest(page, drawer, why.locator('[data-action="approve"]'))
    await expect(why.locator('.sign-in-note')).toHaveCount(1)
    await expect(why.getByText('From a terminal:')).toHaveCount(0)
    // No second, visible explanation further down (the buttons' screen-reader descriptions stay).
    await expect(drawer.getByRole('list', { name: 'Not available to you' })).toHaveCount(0)
    // The tooltips say the same words, each with its own command.
    await expect(why.locator('[data-action="approve"]')).toHaveAttribute('title', note)
    await expect(drawer.locator('[data-section="actions"] [data-action="reject"]')).toHaveAttribute('title',
      `Declining needs sign-in with your Kubernetes identity — run ${command} --decision reject`)
    await axe(page)
  })

  test('the Table: the row\'s Approve is inert, with the same line', async ({ page }) => {
    await page.goto('/ui/#/p/default/payments-service')
    await page.getByRole('radio', { name: 'View: Table' }).click()
    const row = page.getByRole('table').getByRole('row', { name: /^prod/ })
    await honest(page, row, row.locator('[data-action="approve"]'))
    await axe(page)
  })

  test('the Activity tab: Approve, Reject and Revoke mine are inert, with the same line instead of the CLI hint', async ({ page }) => {
    await page.goto('/ui/#/p/default/payments-service')
    await page.getByRole('tab', { name: /^Activity/ }).click()
    const quorum = page.locator('.approval-quorum')
    await honest(page, quorum, quorum.getByRole('button', { name: 'Approve', exact: true }))
    for (const name of ['Reject', 'Revoke mine']) {
      await expect(quorum.getByRole('button', { name, exact: true })).toHaveAttribute('aria-disabled', 'true')
    }
    await expect(quorum.getByText('Or from the CLI:')).toHaveCount(0)
    await axe(page)
  })

  test('a with no environment open, and on Approvals, says the same words; ⌘K lists Approve inert', async ({ page }) => {
    await page.goto('/ui/#/p/default/payments-service')
    await page.getByRole('radio', { name: 'View: Table' }).click()
    await expect(page.getByRole('table')).toBeVisible()
    await page.locator('body').press('a')
    await expect(page.locator('.app-shortcut-note')).toHaveText(note)
    await expect(page.getByTestId('stage-drawer')).toHaveCount(0)

    await page.keyboard.press('Control+k')
    const palette = page.getByRole('dialog', { name: 'Go to or do' })
    await palette.getByRole('combobox').fill('approve 6.14')
    const option = palette.getByRole('option', { name: /Approve 6\.14\.1 for prod/ })
    await expect(option).toHaveAttribute('aria-disabled', 'true')
    await expect(option).toContainText(note)
    await page.keyboard.press('Enter')
    await expect(palette).toBeVisible()
    await page.keyboard.press('Escape')

    await page.goto('/ui/#/approvals')
    await expect(page.getByRole('heading', { level: 1, name: '1 waiting for approval' })).toBeVisible()
    await page.locator('body').press('a')
    await expect(page.locator('.app-shortcut-note')).toHaveText(note)
  })
})

test.describe('Journey 023 — Approve from the keyboard and ⌘K, signed in', () => {
  test.beforeEach(async ({ page }) => { await page.setViewportSize({ width: 1440, height: 900 }) })

  test('a in the Table with no environment open opens the one waiting approval', async ({ page }) => {
    await page.goto('/ui/#/p/default/payments-service')
    await page.getByRole('radio', { name: 'View: Table' }).click()
    await expect(page.getByRole('table')).toBeVisible()
    await page.locator('body').press('a')
    await expect(page).toHaveURL(/\/env\/prod/)
    await expect(page.getByRole('dialog', { name: 'Approve 6.14.1 for prod?' })).toBeVisible()
  })

  test('⌘K: Approve 6.14.1 for prod opens the same dialog', async ({ page }) => {
    await page.goto('/ui/#/p/default/payments-service')
    await expect(page.getByRole('heading', { level: 1, name: 'payments-service' })).toBeVisible()
    await page.keyboard.press('Control+k')
    const palette = page.getByRole('dialog', { name: 'Go to or do' })
    await palette.getByRole('combobox').fill('approve 6.14')
    await expect(palette.getByRole('option', { name: /Approve 6\.14\.1 for prod/ })).not.toHaveAttribute('aria-disabled', 'true')
    await page.keyboard.press('Enter')
    await expect(page.getByRole('dialog', { name: 'Approve 6.14.1 for prod?' })).toBeVisible()
  })
})
