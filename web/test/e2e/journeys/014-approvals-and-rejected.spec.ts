// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 014: an approval gate shows its quorum (one pip per approval
// needed), who approved and whether it counted, and the CLI command to
// approve; a rejected Bundle says who rejected it and why. WCAG 2.1 AA holds
// with both on screen.

import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

test.describe('Journey 014 — Approvals and rejected Bundles', () => {
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
    await expect(page.getByText('kardinal approve payments-service-def456 --env prod')).toBeVisible()

    const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
    expect(results.violations.map(v => v.id)).toEqual([])
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
