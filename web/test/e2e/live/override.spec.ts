// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-OVERRIDE-01 (deprecated): the web UI has no way to approve or override
// a gate; #1245 removed its Override gate dialog, and `kardinal override`
// (CLI) overrides a gate. Go test: TestUI_BrowserNoGateOverride
// (test/e2e/live/ui_browser_test.go).
//
// In KARDINAL_UI_NAMESPACE, podinfo's Bundle is Verified in test and held
// before prod by gate hold-prod (false), whose instance is
// KARDINAL_UI_HOLD_INSTANCE.

import { test, expect, type Page } from '@playwright/test'
import { need, openPipeline, dagNode } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const holdGate = need('KARDINAL_UI_HOLD_GATE')
const holdInstance = need('KARDINAL_UI_HOLD_INSTANCE')

/** The node details compile the gate's expression with this POST; it writes nothing. */
const celCheck = '/api/v1/ui/validate-cel'

/** An approve or override control: a button, link or menu item. */
const approveOrOverride = /approv|overrid/i

async function expectNoOverrideControl(page: Page) {
  for (const role of ['button', 'link', 'menuitem'] as const) {
    await expect(page.getByRole(role, { name: approveOrOverride })).toHaveCount(0)
  }
}

test('a gate that holds the bundle offers no approve or override control', async ({ page }) => {
  const writes: string[] = []
  page.on('request', r => {
    if (r.method() !== 'GET' && !(r.method() === 'POST' && new URL(r.url()).pathname === celCheck)) {
      writes.push(`${r.method()} ${r.url()}`)
    }
  })
  await openPipeline(page, base, ns)

  // The Policy Gates panel opens by itself while the gate holds the bundle.
  const toggle = page.getByRole('button', { name: /Policy Gates \(1\)/ })
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')
  await expect(toggle).toContainText('1 blocked')
  const panel = toggle.locator('..')
  const hold = panel.locator(':scope > div > div').filter({ has: page.getByText(holdInstance, { exact: true }) })
  await expect(hold.locator('.health-chip')).toContainText('Block')
  await expect(dagNode(page, holdGate)).toHaveAttribute('data-health-state', 'Error')
  await expectNoOverrideControl(page)

  await dagNode(page, holdGate).click()
  const detail = page.getByTestId('node-detail')
  await expect(detail).toContainText('Type: PolicyGate')
  await expect(detail).toContainText(holdGate)
  await expectNoOverrideControl(page)

  expect(writes).toEqual([])
})
