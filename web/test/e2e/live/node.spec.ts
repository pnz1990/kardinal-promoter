// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-NODE-01: the node details panel (step progress, events, CEL check), and
// UI-STEPTIME-01: the step timing bars. Go
// test: TestUI_BrowserNodeDetail (test/e2e/live/ui_browser_test.go).
//
// In KARDINAL_UI_NAMESPACE, podinfo's Bundle is Verified in test and waits
// on its prod PR, past gate KARDINAL_UI_GATE (true). Pipeline
// KARDINAL_UI_CEL_PIPELINE's Bundle is held by gate KARDINAL_UI_BAD_GATE,
// whose expression does not compile. The Go test passes each step's
// status.steps, as the controller wrote them.

import { test, expect, type Locator, type Page } from '@playwright/test'
import { need, needJSON, openPipeline, dagNode } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const gate = need('KARDINAL_UI_GATE')
const celPipeline = need('KARDINAL_UI_CEL_PIPELINE')
const badGate = need('KARDINAL_UI_BAD_GATE')
const badExpression = need('KARDINAL_UI_BAD_EXPRESSION')
const prURL = need('KARDINAL_UI_PR_URL')

interface SubStep { name: string; state: 'Pending' | 'InProgress' | 'Completed' | 'Failed' }
const testSteps = needJSON<SubStep[]>('KARDINAL_UI_TEST_STEPS')
const prodSteps = needJSON<SubStep[]>('KARDINAL_UI_PROD_STEPS')

/** What a screen reader hears for each sub-step state. */
const spoken: Record<SubStep['state'], string> = {
  Completed: 'done', InProgress: 'running', Failed: 'failed', Pending: 'not started',
}

const details = (page: Page) => page.getByTestId('node-detail')

async function expectSubSteps(detail: Locator, want: SubStep[]) {
  const items = detail.getByRole('list', { name: 'Promotion steps' }).getByRole('listitem')
  await expect(items).toHaveCount(want.length)
  for (const [i, s] of want.entries()) {
    await expect(items.nth(i)).toHaveAttribute('data-step-state', s.state)
    await expect(items.nth(i)).toContainText(s.name)
    await expect(items.nth(i)).toContainText(spoken[s.state])
  }
}

test('a step shows its sub-steps, elapsed time, merge link and events', async ({ page }) => {
  await openPipeline(page, base, ns)
  await expect(details(page)).toHaveCount(0)

  await dagNode(page, 'prod').click()
  const detail = details(page)
  await expect(detail.getByRole('heading', { level: 3 })).toHaveText('prod')
  await expect(detail).toContainText('Type: PromotionStep')
  await expect(detail).toContainText('Environment: prod')
  await expect(detail.locator('.health-chip').first()).toHaveAttribute('data-health-state', 'Reconciling')
  await expectSubSteps(detail, prodSteps)
  await expect(detail.locator('li[data-step-state="InProgress"]')).toContainText('waiting for merge')

  // The elapsed time ticks while the panel is open.
  const elapsed = detail.locator('strong', { hasText: 'Elapsed:' }).locator('..')
  await expect(elapsed).toHaveText(/^Elapsed: (\d+s|\d+m \d+s)$/)
  const first = await elapsed.textContent()
  await expect.poll(() => elapsed.textContent(), { timeout: 5_000 }).not.toBe(first)

  const merge = detail.getByRole('link', { name: 'Open Pull Request — Merge to Deploy' })
  await expect(merge).toHaveAttribute('href', prURL)
  await expect(merge).toHaveAttribute('target', '_blank')

  const events = detail.getByTestId('events-panel')
  await expect(events.getByRole('heading', { level: 4 })).toHaveText(/^Events \(\d+\)$/)
  const waiting = events.getByTestId('event-row').filter({ hasText: 'WaitingForMerge' })
  await expect(waiting.first()).toContainText(prURL)
  await expect(waiting.first().getByTestId('event-type')).toHaveText('Normal')

  // A finished step: every sub-step done, no elapsed time, no merge link.
  await dagNode(page, 'test').click()
  await expect(detail).toContainText('Environment: test')
  await expect(detail.locator('.health-chip').first()).toHaveAttribute('data-health-state', 'Ready')
  await expectSubSteps(detail, testSteps)
  // UI-STEPTIME-01: each finished sub-step is a bar on the promotion's time
  // span, in the order the steps ran, the last one ending at the span's end.
  const bars = detail.getByRole('list', { name: 'Promotion steps' }).getByTestId('step-bar')
  await expect(bars).toHaveCount(testSteps.filter(s => s.state === 'Completed').length)
  const spans = await bars.evaluateAll(els => els.map(e => ({
    offset: Number(e.getAttribute('data-offset')), width: Number(e.getAttribute('data-width')),
  })))
  for (let i = 1; i < spans.length; i++) expect(spans[i].offset).toBeGreaterThanOrEqual(spans[i - 1].offset)
  expect(spans[0].offset).toBe(0)
  expect(Math.max(...spans.map(s => s.offset + s.width))).toBeGreaterThanOrEqual(99)
  await expect(detail.locator('strong', { hasText: 'Elapsed:' })).toHaveCount(0)
  await expect(detail.getByRole('link', { name: /Pull Request/ })).toHaveCount(0)
  const verified = detail.getByTestId('event-row').filter({ hasText: 'Verified' })
  await expect(verified.first()).toContainText('env test: step completed successfully')

  await detail.getByRole('button', { name: 'Close' }).click()
  await expect(details(page)).toHaveCount(0)
})

test('a gate shows its CEL expression, checked by the controller', async ({ page }) => {
  await openPipeline(page, base, ns)
  await dagNode(page, gate).click()
  const detail = details(page)
  await expect(detail).toContainText('Type: PolicyGate')
  await expect(detail.getByRole('heading', { level: 4, name: 'CEL Expression' })).toBeVisible()
  await expect(detail.getByText('✓ valid', { exact: true })).toBeVisible()
  await expect(detail.getByText('✗ error', { exact: true })).toHaveCount(0)
  await expect(detail).toContainText('Last evaluated:')

  // An expression that does not compile: the chip and the compile error.
  await openPipeline(page, base, ns, celPipeline)
  await dagNode(page, badGate).click()
  await expect(detail).toContainText('Type: PolicyGate')
  await expect(detail).toContainText(badExpression)
  const chip = detail.getByText('✗ error', { exact: true })
  await expect(chip).toBeVisible()
  await expect(chip).toHaveAttribute('title', /^CEL compile error: /)
  const error = await chip.getAttribute('title')
  await expect(detail.getByText(error!, { exact: true })).toBeVisible()
  await expect(detail.getByText('✓ valid', { exact: true })).toHaveCount(0)
})
