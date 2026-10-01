// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-GATES-01: the Policy Gates panel, the blocking banner and the Promotion
// Errors panel. Go test: TestUI_BrowserGates (test/e2e/live/ui_browser_test.go).
//
// In KARDINAL_UI_NAMESPACE, podinfo's Bundle is Verified in test, past gate
// check-test (true), and held before prod by gate hold-prod (false). In
// KARDINAL_UI_FAILED_NAMESPACE, podinfo's Bundle failed in its only
// environment, whose health check kardinal refuses. The DAG names a gate by
// its template; the Policy Gates panel lists the Bundle's gate instances.

import { test, expect, type Page } from '@playwright/test'
import { need, openPipeline, dagNode } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const holdGate = need('KARDINAL_UI_HOLD_GATE')
const holdInstance = need('KARDINAL_UI_HOLD_INSTANCE')
const holdReason = need('KARDINAL_UI_HOLD_REASON')
const passGate = need('KARDINAL_UI_PASS_GATE')
const passInstance = need('KARDINAL_UI_PASS_INSTANCE')
const failedNs = need('KARDINAL_UI_FAILED_NAMESPACE')
const failedEnv = need('KARDINAL_UI_FAILED_ENV')
const failedMessage = need('KARDINAL_UI_FAILED_MESSAGE')

/** The banner about gates holding the bundle (the page has other alerts). */
const blockedBanner = (page: Page) => page.getByRole('alert').filter({ hasText: /PolicyGates? blocking promotion/ })

test('a gate that holds the bundle shows in the banner, the DAG and the Policy Gates panel', async ({ page }) => {
  await openPipeline(page, base, ns)

  const banner = blockedBanner(page)
  await expect(banner).toContainText('1 PolicyGate blocking promotion')
  const highlight = banner.getByRole('button')
  await expect(highlight).toHaveText('Show blocked')
  await expect(highlight).toHaveAttribute('aria-pressed', 'false')
  await expect(dagNode(page, holdGate)).toHaveAttribute('data-health-state', 'Error')
  await expect(dagNode(page, holdGate)).not.toHaveClass(/dag-node--highlighted/)

  // The toggle highlights the gate that holds the bundle, not the one it passed.
  await highlight.click()
  await expect(highlight).toHaveText('Show all')
  await expect(highlight).toHaveAttribute('aria-pressed', 'true')
  await expect(dagNode(page, holdGate)).toHaveClass(/dag-node--highlighted/)
  await expect(dagNode(page, passGate)).not.toHaveClass(/dag-node--highlighted/)
  await highlight.click()
  await expect(highlight).toHaveText('Show blocked')
  await expect(dagNode(page, holdGate)).not.toHaveClass(/dag-node--highlighted/)

  // The panel opens by itself while a gate holds the bundle.
  const toggle = page.getByRole('button', { name: /Policy Gates \(2\)/ })
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')
  await expect(toggle).toContainText('1 blocked')
  const panel = toggle.locator('..')
  const row = (name: string) => panel.locator(':scope > div > div').filter({ has: page.getByText(name, { exact: true }) })

  const hold = row(holdInstance)
  await expect(hold.locator('.health-chip')).toHaveAttribute('data-health-state', 'Error')
  await expect(hold.locator('.health-chip')).toContainText('Block')
  await expect(hold.locator('code')).toHaveText('false')
  await expect(hold).toContainText(holdReason)
  await expect(hold).toContainText(`${ns} ·`)

  const pass = row(passInstance)
  await expect(pass.locator('.health-chip')).toHaveAttribute('data-health-state', 'Ready')
  await expect(pass.locator('.health-chip')).toContainText('Pass')
  await expect(pass.locator('code')).toHaveText('true')
  await expect(pass).not.toContainText(holdReason)

  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await expect(row(holdInstance)).toHaveCount(0)
  await toggle.click()
  await expect(row(holdInstance)).toHaveCount(1)

  // Nothing failed here.
  await expect(page.getByTestId('promotion-errors-panel')).toHaveCount(0)
})

test('a failed promotion shows in the Promotion Errors panel, whose link opens its step', async ({ page }) => {
  await openPipeline(page, base, failedNs)
  await expect(blockedBanner(page)).toHaveCount(0)

  const panel = page.getByTestId('promotion-errors-panel')
  await expect(panel).toContainText('1 environment failed')
  await expect(panel).toContainText('1 distinct failure pattern')
  const group = panel.getByTestId('error-group')
  await expect(group).toHaveCount(1)
  // No sub-step ran: the step was refused before it started.
  await expect(group.getByTestId('error-step-type')).toHaveText('promotion')
  await expect(group.getByTestId('error-count')).toHaveText('1×')
  await expect(group).toContainText(failedMessage)

  const expand = group.getByTestId('expand-environments')
  await expect(expand).toHaveText('1 affected environment ▸')
  await expect(group.getByTestId('environment-link')).toHaveCount(0)
  await expand.click()
  await expect(expand).toHaveText('Collapse')
  const link = group.getByTestId('environment-link')
  await expect(link).toHaveCount(1)
  await expect(link).toContainText(failedEnv)
  await expect(link).toContainText(`(${failedNs})`)

  const detail = page.getByTestId('node-detail')
  await expect(detail).toHaveCount(0)
  await link.click()
  await expect(detail).toContainText(`Environment: ${failedEnv}`)
  await expect(detail).toContainText('Type: PromotionStep')
  await expect(detail.locator('.health-chip').first()).toHaveAttribute('data-health-state', 'Error')
  await expect(detail).toContainText(`Message: ${failedMessage}`)
})
