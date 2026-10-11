// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Journey 018 — pipelines in two namespaces: the sidebar groups them under a header per
// namespace, each group a list named by its namespace, and the page passes axe (a nested
// list with role="group" left its items outside any list: axe's listitem rule).
import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

test.describe('Journey 018 — Two namespaces', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/ui/pipelines', async (route) => {
      const list = await (await route.fetch()).json()
      await route.fulfill({ json: [...list, { ...list[0], namespace: 'team-b' }] })
    })
  })

  test('the sidebar groups pipelines by namespace in named lists, and passes axe', async ({ page }) => {
    await page.goto('/ui/#/')
    const sidebar = page.getByRole('list', { name: 'Pipelines' })
    await expect(sidebar.getByRole('list', { name: 'default' })).toBeVisible()
    const teamB = sidebar.getByRole('list', { name: 'team-b' })
    await expect(teamB.getByRole('listitem')).toHaveCount(1)
    await expect(teamB).toContainText('kardinal-test-app')
    const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).include('.app-sidebar').analyze()
    expect(results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([])
  })
})
