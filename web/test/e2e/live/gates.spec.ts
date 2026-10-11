// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-GATES-01: the gates panel (the Activity tab), the map's attention
// strip and gate marks, and the Promotion Errors panel. Go test: TestUI_BrowserGates (test/e2e/live/ui_browser_test.go).
//
// In KARDINAL_UI_NAMESPACE, podinfo's Bundle is Verified in test, past gate
// check-test (true), and held before prod by gate hold-prod (false). In
// KARDINAL_UI_FAILED_NAMESPACE, podinfo's Bundle failed in its only
// environment, whose health check kardinal refuses. The DAG names a gate by
// its template; the gates panel lists the Bundle's gate instances.

import { test, expect, type Page } from '@playwright/test'
import { need, openPipeline, station, gateMark, stageDrawer, drawerHeading } from './live'

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

/** The map's attention strip item for the gates holding the bundle. */
const blockedItem = (page: Page) => page.getByRole('group', { name: 'Needs attention' }).getByRole('button', { name: /blocked by/ })

test('a gate that holds the bundle shows in the attention strip, on the map and in the Policy Gates panel', async ({ page }) => {
  await openPipeline(page, base, ns)

  // The attention strip names the gate; the map bars the edge into prod.
  await expect(blockedItem(page)).toHaveText(`1 blocked by ${holdGate}`)
  await expect(gateMark(page, holdGate)).toHaveAttribute('data-tone', 'blocked')
  await expect(gateMark(page, passGate)).toHaveAttribute('data-tone', 'verified')
  await expect(station(page, 'prod')).toHaveAttribute('aria-label', new RegExp(`blocked by ${holdGate}`))
  // The item takes you to the held environment.
  await blockedItem(page).click()
  await expect(station(page, 'prod')).toBeFocused()

  // The version's gates are on the Activity tab.
  await page.getByRole('tab', { name: /^Activity/ }).click()
  // The panel opens by itself while a gate holds the bundle.
  const toggle = page.getByRole('button', { name: /Gates \(2\)/ })
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
  await page.getByRole('tab', { name: 'Map' }).click()
  await expect(page.getByTestId('promotion-errors-panel')).toHaveCount(0)
})

test('a failed promotion shows in the Promotion Errors panel, whose link opens its step', async ({ page }) => {
  await openPipeline(page, base, failedNs)
  await expect(blockedItem(page)).toHaveCount(0)

  // Promotion errors are on the Activity tab (U1).
  await page.getByRole('tab', { name: /^Activity/ }).click()
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

  // The link opens the environment's stage drawer: why it's waiting is the failure.
  const detail = stageDrawer(page)
  await expect(detail).toHaveCount(0)
  await link.click()
  await expect(drawerHeading(page)).toHaveText(failedEnv)
  await expect(detail.locator('.stage-drawer__status .stage-chip')).toHaveText(/Failed/)
  await expect(detail.locator('[data-section="why"] .stage-why__item--failed')).toContainText(failedMessage)
  await expect(detail.locator('details.k-k8s').last()).toContainText('KindPromotionStep')
})
