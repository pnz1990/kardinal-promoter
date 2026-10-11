// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-NODE-01: an environment's details in the stage drawer (step progress,
// events, CEL check), and UI-STEPTIME-01: the step timing bars. Go
// test: TestUI_BrowserNodeDetail (test/e2e/live/ui_browser_test.go).
//
// In KARDINAL_UI_NAMESPACE, podinfo's Bundle is Verified in test and waits
// on its prod PR, past gate KARDINAL_UI_GATE (true). Pipeline
// KARDINAL_UI_CEL_PIPELINE's Bundle is held by gate KARDINAL_UI_BAD_GATE,
// whose expression does not compile. The Go test passes each step's
// status.steps, as the controller wrote them.

import { test, expect, type Locator, type Page } from '@playwright/test'
import { need, needJSON, openPipeline, station, gateMark, stageDrawer, drawerHeading } from './live'

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

const details = (page: Page) => stageDrawer(page)
const section = (detail: Locator, name: 'why' | 'actions' | 'evidence' | 'history') => detail.locator(`[data-section="${name}"]`)

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

  await station(page, 'prod').click()
  const detail = details(page)
  await expect(drawerHeading(page)).toHaveText('prod')
  await expect(detail.locator('.stage-drawer__status .stage-chip')).toHaveText(/Waiting for PR merge/)
  await expect(detail.locator('details.k-k8s').last()).toContainText('KindPromotionStep')
  await expectSubSteps(detail, prodSteps)
  await expect(detail.locator('li[data-step-state="InProgress"]')).toContainText('waiting for merge')

  // The elapsed time ticks while the drawer is open.
  const elapsed = detail.getByTestId('stage-elapsed')
  await expect(elapsed).toHaveText(/^Running for (\d+s|\d+m \d+s|\d+h \d+m)$/)
  const first = await elapsed.textContent()
  await expect.poll(() => elapsed.textContent(), { timeout: 5_000 }).not.toBe(first)

  // Why it's waiting: its PR, with the link to merge it.
  const merge = section(detail, 'why').getByRole('link', { name: 'Open pull request' })
  await expect(merge).toHaveAttribute('href', prURL)
  await expect(merge).toHaveAttribute('target', '_blank')

  const events = section(detail, 'history').getByTestId('events-panel')
  await expect(events.getByRole('heading', { level: 4 })).toHaveText(/^Events \(\d+\)$/)
  const waiting = events.getByTestId('event-row').filter({ hasText: 'WaitingForMerge' })
  await expect(waiting.first()).toContainText(prURL)
  await expect(waiting.first().getByTestId('event-type')).toHaveText('Normal')

  // A finished step: every sub-step done, no elapsed time, no merge link.
  await station(page, 'test').click()
  await expect(drawerHeading(page)).toHaveText('test')
  await expect(detail.locator('.stage-drawer__status .stage-chip')).toHaveText(/Live/)
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
  await expect(detail.getByTestId('stage-elapsed')).toHaveCount(0)
  await expect(detail.getByRole('link', { name: /pull request/i })).toHaveCount(0)
  const verified = detail.getByTestId('event-row').filter({ hasText: 'Verified' })
  await expect(verified.first()).toContainText('env test: step completed successfully')

  await detail.getByRole('button', { name: 'Close test' }).click()
  await expect(details(page)).toHaveCount(0)
})

test('a gate shows its CEL expression, checked by the controller', async ({ page }) => {
  // A gate opens the drawer of the environment it gates; a passing gate is in its evidence.
  await openPipeline(page, base, ns)
  await gateMark(page, gate).click() // a gate on the map opens its own details
  const detail = details(page)
  await expect(drawerHeading(page)).toHaveText('prod')
  const passing = section(detail, 'evidence').locator('.stage-gates > li').filter({ hasText: gate })
  await expect(passing).toContainText('Passing')
  // When the controller last evaluated it.
  await expect(passing).toContainText(/ · (\d+s|\d+m \d+s|\d+h \d+m) ago/)
  await passing.getByText('Details', { exact: true }).click()
  await expect(passing.locator('[data-cel-check="valid"]')).toHaveText('✓valid')
  await expect(passing.locator('[data-cel-check="error"]')).toHaveCount(0)

  // An expression that does not compile holds its environment: why it's waiting names it,
  // and its Details show the compile error.
  await openPipeline(page, base, ns, celPipeline)
  await gateMark(page, badGate).click()
  const why = section(detail, 'why').locator('.stage-why__item').filter({ hasText: badGate })
  // When the controller last evaluated it: the Go test waits until it has.
  await expect(why).toContainText(/evaluated (\d+s|\d+m \d+s|\d+h \d+m) ago/)
  await expect(why.locator('details.k-k8s')).toContainText('KindPolicyGate')
  await why.getByText('Details', { exact: true }).click()
  await expect(why).toContainText(badExpression)
  const chip = why.locator('[data-cel-check="error"]')
  await expect(chip).toHaveText('✗error')
  await expect(chip).toHaveAttribute('title', /^CEL compile error: /)
  const error = await chip.getAttribute('title')
  await expect(why.getByText(error!, { exact: true })).toBeVisible()
  await expect(why.locator('[data-cel-check="valid"]')).toHaveCount(0)
})
