// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 012: the fleet board — with no pipeline selected, every pipeline is
// a line of stations showing the version each environment runs and the
// release on its way; a station opens its pipeline, and the logo returns.

import { test, expect } from '@playwright/test'

test.describe('Journey 012 — Fleet board', () => {
  test('shows what each environment runs and the release on its way', async ({ page }) => {
    await page.goto('/')
    const board = page.getByRole('region', { name: 'Fleet' })
    await expect(board).toBeVisible()
    const prod = board.getByRole('button', { name: /^kardinal-test-app prod:/ })
    await expect(prod).toHaveAttribute('data-state', 'arriving')
    await expect(prod).toContainText('sha-9f8e7d6')
    await expect(prod).toContainText('waiting for merge')
    await expect(board.locator('.fleet-rail__version', { hasText: 'sha-abc1234' })).toBeVisible()
    const uat = board.getByRole('button', { name: /^kardinal-test-app uat:/ })
    await expect(uat).toContainText(/verified \d+m ago/)
    // An image Bundle runs with the config of the last config Bundle (#1353).
    await expect(uat).toContainText('+ config 77aa001')
    await expect(uat).toHaveAccessibleName(/with config 77aa001 from kardinal-test-app-cfg9/)
  })

  test('a station opens its pipeline and the logo returns to the fleet', async ({ page }) => {
    await page.goto('/')
    await page.getByRole('region', { name: 'Fleet' }).getByRole('button', { name: /^kardinal-test-app uat:/ }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'kardinal-test-app' })).toBeVisible()
    await page.getByRole('button', { name: 'Show the fleet' }).click()
    await expect(page.getByRole('region', { name: 'Fleet' })).toBeVisible()
    await expect(page).not.toHaveURL(/pipeline=/)
  })
})
