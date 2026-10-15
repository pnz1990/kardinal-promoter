// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 025 (#1908): who shipped a version reads the same everywhere. The
// mock's payments-service 6.14.1 was shipped by agent fixer for alice, with a
// model it asserted; kardinal-test-app's prod runs a version bob shipped from
// the UI. The ActorBadge names them in the stage drawer and the Versions
// strip; the fleet board and the map mark an agent's version in words; the
// Agents filter keeps the pipelines an agent shipped to. Each surface passes axe.

import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

async function axe(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target).join(' ')}`)).toEqual([])
}

test.describe('Journey 025 — who shipped a version', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
  })

  test('the stage drawer names the agent, its sponsor and what it asserted', async ({ page }) => {
    await page.goto('/ui/#/p/default/payments-service/env/staging')
    const drawer = page.getByTestId('stage-drawer')
    const shipped = drawer.locator('.stage-versions .k-actor')
    await expect(shipped).toHaveAttribute('data-actor-kind', 'agent')
    await expect(shipped).toHaveText('agent fixer for alice')
    await expect(shipped).toHaveAttribute('title', 'agent fixer for alice · model claude-opus-5 (asserted)')
    await expect(drawer.locator('.stage-versions')).toContainText('model claude-opus-5 (asserted)')
    await axe(page)
  })

  test('the map marks the agent\'s version in words', async ({ page }) => {
    await page.goto('/ui/#/p/default/payments-service')
    await expect(page.locator('.station-map')).toBeVisible()
    await expect(page.locator('.station-map').getByText(/6\.14\.1 · agent/).first()).toBeVisible()
    await axe(page)
  })

  test('the Agents filter keeps the pipelines an agent shipped to, and deep-links', async ({ page }) => {
    await page.goto('/ui/')
    const agents = page.getByRole('button', { name: '1 pipeline running or receiving a version an agent shipped' })
    await agents.click()
    await expect(page).toHaveURL(/filter=agents/)
    await expect(page.locator('.fleet-board').getByText('payments-service')).toBeVisible()
    await expect(page.locator('.fleet-board').getByText('kardinal-test-app')).toHaveCount(0)
    await axe(page)
    await page.goto('/ui/#/?filter=agents')
    await expect(page.getByRole('button', { name: '1 pipeline running or receiving a version an agent shipped' })).toHaveAttribute('aria-pressed', 'true')
  })

  test('a person who shipped from the UI is named on the version strip', async ({ page }) => {
    await page.goto('/ui/#/p/default/kardinal-test-app')
    const chip = page.locator('[title^="kardinal-test-app-prev111:"]')
    await expect(chip).toHaveAttribute('title', /shipped by bob via the UI/)
    await expect(chip.locator('.bundle-chip__agent')).toHaveCount(0)
    await axe(page)
  })
})
