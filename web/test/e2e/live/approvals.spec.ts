// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-APPROVALS-01: an approval gate shows its quorum, who approved and
// whether it counted, and the CLI command to approve. Go test:
// TestUI_BrowserApprovals (test/e2e/live/ui_browser_test.go).
//
// In KARDINAL_UI_NAMESPACE, podinfo's Bundle KARDINAL_UI_BUNDLE waits at
// gate two-approvers on prod (2 approvals from release-managers):
// KARDINAL_UI_APPROVER approved (counted), KARDINAL_UI_OUTSIDER approved but
// is not in the group (not counted).

import { test, expect } from '@playwright/test'
import { need, openPipeline } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const bundle = need('KARDINAL_UI_BUNDLE')
const approver = need('KARDINAL_UI_APPROVER')
const outsider = need('KARDINAL_UI_OUTSIDER')

test('an approval gate shows its quorum and who approved', async ({ page }) => {
  await openPipeline(page, base, ns)
  const meter = page.getByRole('meter', { name: 'Approvals' })
  await expect(meter).toBeVisible() // the gate holds the Bundle: the panel is open
  await expect(meter).toHaveAttribute('aria-valuetext', '1 of 2 approvals')
  await expect(meter).toHaveAttribute('aria-valuemax', '2')
  await expect(page.getByText("From members of release-managers; not the Bundle's creator")).toBeVisible()
  const rows = page.getByRole('list', { name: 'Approval decisions' }).getByRole('listitem')
  await expect(rows).toHaveCount(2)
  const counted = rows.filter({ hasText: approver })
  await expect(counted).toHaveAttribute('data-counted', 'true')
  await expect(counted).toContainText('approved')
  await expect(counted).toContainText('canary looks clean')
  const notCounted = rows.filter({ hasText: outsider })
  await expect(notCounted).toHaveAttribute('data-counted', 'false')
  await expect(notCounted).toContainText('not counted')
  await expect(page.getByText(`kardinal approve ${bundle} --env prod`)).toBeVisible()
})
