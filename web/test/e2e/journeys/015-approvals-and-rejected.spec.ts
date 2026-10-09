// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 015: an approval gate shows its quorum (one pip per approval
// needed), who approved and whether it counted, and the CLI command to
// approve; a rejected Bundle says who rejected it and why. WCAG 2.1 AA holds
// with both on screen.

import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

test.describe('Journey 015 — Approvals and rejected Bundles', () => {
  test('an approval gate shows its quorum and decisions', async ({ page }) => {
    await page.goto('/')
    await page.locator('aside').getByText('payments-service').first().click()
    const meter = page.getByRole('meter', { name: 'Approvals' })
    await expect(meter).toBeVisible() // a holding gate opens the panel
    await expect(meter).toHaveAttribute('aria-valuetext', '1 of 2 approvals')
    await expect(page.locator('.approval-quorum__pip[data-filled]')).toHaveCount(1)
    await expect(page.getByText("From members of release-managers; not the Bundle's creator")).toBeVisible()
    const rows = page.getByRole('list', { name: 'Approval decisions' }).getByRole('listitem')
    await expect(rows).toHaveCount(2)
    await expect(rows.nth(0)).toContainText('alice')
    await expect(rows.nth(0)).toContainText('canary looks clean')
    await expect(rows.nth(1)).toContainText("not counted: the Bundle's creator (excludeAuthor)")
    await expect(page.getByText('kardinal approve payments-service-def456 --env prod -n default')).toBeVisible()

    const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
    expect(results.violations.map(v => v.id)).toEqual([])
  })

  test('approves from the UI, and asks before rejecting', async ({ page }) => {
    await page.goto('/')
    await page.locator('aside').getByText('payments-service').first().click()
    await page.getByLabel('Comment (optional)').fill('checked the dashboards')
    await page.getByRole('button', { name: 'Approve', exact: true }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Recorded: carol approves payments-service-def456 for prod' }))
      .toBeVisible()
    await page.getByRole('button', { name: 'Reject', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Reject payments-service-def456 for prod?' })
    await expect(dialog).toBeVisible()
    const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
    expect(results.violations.map(v => v.id)).toEqual([])
    await dialog.getByRole('button', { name: 'Reject' }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Recorded: carol rejects' })).toBeVisible()
  })

  test('a rejected Bundle says who rejected it and why', async ({ page }) => {
    await page.goto('/')
    await page.locator('aside').getByText('payments-service').first().click()
    const chip = page.locator('.bundle-chip[data-bundle-phase="Rejected"]')
    await expect(chip).toHaveClass(/bundle-chip--rejected/)
    await chip.click()
    await expect(page.getByRole('note')).toHaveText(/^Rejected by bob \d+m ago: CVE-2026-1234 in the base image$/)
  })
})
