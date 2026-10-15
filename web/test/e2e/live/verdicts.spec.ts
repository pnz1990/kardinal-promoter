// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-VERDICTS-01: a verify gate's verdicts in the stage drawer and on the
// Activity tab, each with its verifier's actor badge, its result in words
// and its evidence link; an inconclusive verdict says what it does. Go test:
// TestUI_BrowserVerdicts (test/e2e/live/verify_test.go).
//
// In KARDINAL_UI_NAMESPACE, podinfo's prod waits at verify gate explore-qa
// (check explore in uat, 2 passing verdicts, onInconclusive hold):
// KARDINAL_UI_PASSER passed with the link KARDINAL_UI_EVIDENCE and one high
// finding; KARDINAL_UI_INCONCLUSIVE was inconclusive.

import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { need, openPipeline, pipelineHash, stageDrawer, PIPELINE } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const passer = need('KARDINAL_UI_PASSER')
const inconclusive = need('KARDINAL_UI_INCONCLUSIVE')
const evidence = need('KARDINAL_UI_EVIDENCE')

async function axe(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target).join(' ')}`)).toEqual([])
}

async function checkVerdicts(scope: ReturnType<Page['locator']>) {
  const meter = scope.getByRole('meter', { name: 'Verdicts' })
  await expect(meter).toHaveAttribute('aria-valuetext', '1 of 2 verdicts for explore; 1 inconclusive, waiting')
  const rows = scope.getByRole('list', { name: 'Verdicts' }).getByRole('listitem')
  await expect(rows).toHaveCount(2)
  const pass = rows.filter({ hasText: passer })
  await expect(pass.locator('.k-actor')).toHaveAttribute('data-actor-kind', 'human')
  await expect(pass.locator('.k-chip')).toHaveText('✓Pass')
  await expect(pass).toContainText('1 finding, 1 high')
  await expect(pass.getByRole('link', { name: `Evidence from ${passer} (opens in a new tab)` })).toHaveAttribute('href', evidence)
  const inc = rows.filter({ hasText: inconclusive })
  await expect(inc.locator('.k-actor')).toHaveAttribute('data-actor-kind', 'human')
  await expect(inc.locator('.k-chip')).toHaveText('?Inconclusive (holds)')
  await expect(inc).toHaveAttribute('data-counted', 'false')
}

test('the stage drawer shows the verdicts prod waits for', async ({ page }) => {
  await page.goto(`${base}/ui/${pipelineHash(ns)}/env/prod`)
  const why = stageDrawer(page).locator('[data-section="why"]')
  await expect(why).toContainText('Needs 1 more passing verdict (1 of 2) for explore in explore-qa.')
  await checkVerdicts(why)
  await expect(why.getByText(`kardinal verify ${PIPELINE} uat --check explore --pass`, { exact: false })).toBeVisible()
  await axe(page)
})

test('the Activity tab shows the same verdicts', async ({ page }) => {
  await openPipeline(page, base, ns)
  await page.getByRole('tab', { name: /^Activity/ }).click()
  await checkVerdicts(page.locator('.approval-quorum').filter({ has: page.getByRole('meter', { name: 'Verdicts' }) }))
  await axe(page)
})
