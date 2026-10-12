// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-INBOX-01: the approvals inbox (#/approvals). Go test:
// TestUI_BrowserApprovals (test/e2e/live/ui_browser_test.go).
//
// In KARDINAL_UI_NAMESPACE, podinfo's prod waits at gate two-approvers (2
// approvals from release-managers); KARDINAL_UI_APPROVER approved it with the
// CLI. This UI does not run TokenReview, so it does not know who is at it:
// the inbox lists the approval, says it cannot record one, and keeps Approve
// and Decline in place but inert.

import { test, expect } from '@playwright/test'
import { need, PIPELINE } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const approver = need('KARDINAL_UI_APPROVER')

test('the inbox lists the approval waiting in prod; without sign-in it cannot record one', async ({ page }) => {
  const writes: string[] = []
  page.on('request', r => {
    const path = new URL(r.url()).pathname
    if (r.method() !== 'GET' && path !== '/api/v1/ui/access') writes.push(`${r.method()} ${path}`)
  })
  await page.goto(`${base}/ui/#/approvals`)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(/^\d+ waiting for approval$/)
  await expect(page.getByText(/The UI does not know who you are/)).toBeVisible()
  const row = page.getByRole('list', { name: 'Waiting on you' }).getByRole('listitem')
    .filter({ has: page.getByRole('link', { name: `${PIPELINE}, prod` }) })
    .filter({ has: page.locator(`a[href^="#/p/${ns}/"]`) })
  await expect(row).toHaveCount(1)
  await expect(row).toContainText('two-approvers')
  await expect(row.getByRole('img', { name: '1 of 2 approvals' })).toBeVisible()
  await expect(row).toContainText(`1 of 2 · ${approver} approved`)
  const approve = row.getByRole('button', { name: 'Approve' })
  await expect(approve).toHaveAttribute('aria-disabled', 'true')
  await expect(approve).toHaveAttribute('title', `Approving needs sign-in with your Kubernetes identity — run kardinal approve ${PIPELINE} prod -n ${ns}`)
  await approve.click({ force: true })
  await expect(page.getByRole('dialog')).toHaveCount(0)

  // The row opens prod's drawer at why it's waiting.
  await row.getByRole('link', { name: `${PIPELINE}, prod` }).click()
  await expect(page.getByTestId('stage-drawer').getByRole('heading', { level: 2 })).toHaveText('prod')
  expect(writes).toEqual([])
})
