// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-REJECTED-01: a rejected Bundle says who rejected it and why, and its
// timeline chip is Rejected. Go test: TestUI_BrowserApprovals.

import { test, expect } from '@playwright/test'
import { need, openPipeline } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const bundle = need('KARDINAL_UI_BUNDLE')
const rejecter = need('KARDINAL_UI_REJECTER')
const reason = need('KARDINAL_UI_REASON')

test('a rejected Bundle says who rejected it and why', async ({ page }) => {
  await openPipeline(page, base, ns)
  const chip = page.locator(`.bundle-chip[title^="${bundle}: "]`)
  await expect(chip).toHaveAttribute('data-bundle-phase', 'Rejected')
  await expect(chip).toHaveClass(/bundle-chip--rejected/)
  await expect(page.getByRole('note')).toHaveText(new RegExp(`^Rejected by ${rejecter.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')} (just now|\\d+m ago): ${reason}$`))
  // A rejected Bundle is never promoted: the approval gate no longer holds it.
  await expect(page.getByRole('meter', { name: 'Approvals' })).toHaveCount(0)
})
