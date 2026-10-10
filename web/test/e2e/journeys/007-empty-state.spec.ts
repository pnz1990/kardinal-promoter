// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 007: Empty state — no pipelines → onboarding card.
// The empty list comes from page.route, which overrides the mock server's
// pipeline list for this test.

import { test, expect } from '@playwright/test'

test.describe('Journey 007 — Empty state onboarding', () => {
  test('Step 1: Default state shows pipelines (not empty)', async ({ page }) => {
    await page.goto('/')
    // Fixture returns 2 pipelines, so should NOT show empty state
    await expect(page.getByRole('complementary').getByText('kardinal-test-app')).toBeVisible()
    await expect(page.getByText(/No pipelines found/i)).toHaveCount(0)
  })

  test('Step 2: With pipelines but none selected, the main area shows the fleet board', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('heading', { name: 'Fleet' })).toBeVisible()
  })

  test('Step 3: An empty cluster shows the onboarding card with the kubectl apply command', async ({ page }) => {
    await page.route('**/api/v1/ui/pipelines', route => route.fulfill({ json: [] }))
    await page.goto('/')
    const card = page.getByTestId('empty-state')
    await expect(card).toContainText('No pipelines found')
    await expect(card.getByTestId('quickstart-command')).toHaveText(
      'kubectl apply -f https://raw.githubusercontent.com/pnz1990/kardinal-promoter/main/examples/quickstart/pipeline.yaml')
    await expect(page.getByRole('heading', { name: 'Fleet' })).toHaveCount(0)
  })
})
