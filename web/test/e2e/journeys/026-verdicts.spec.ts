// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 026 (#1908): a verify gate's verdicts (docs/agentic/verify-with-agents.md).
// The journey puts a verify gate on payments-service prod, next to its
// approval gate: one pass from agent qa-explorer for bob (counted, with its
// evidence), one inconclusive one it retries, and one from the version's own
// author that does not count. The stage drawer says how many passing verdicts
// it still needs and shows the quorum; each verdict names its verifier with
// the ActorBadge, its result in words, and an evidence link named for who
// wrote it. The Activity tab shows the same panel. Each surface passes axe.

import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

async function axe(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target).join(' ')}`)).toEqual([])
}

const ago = (ms: number) => new Date(Date.now() - ms).toISOString()

/** The verify gate, as the UI API serves it (ui_api.go uiGateVerify). */
function verifyGate() {
  return {
    name: 'exploratory-qa-payments-service-def456-prod', namespace: 'default', pipeline: 'payments-service',
    bundle: 'payments-service-def456', environment: 'prod', gateTemplate: 'exploratory-qa', maxOverrideMinutes: 1440,
    expression: 'verdicts.passed >= verdicts.required', ready: false, holding: true, state: 'Block',
    reason: 'waiting for verdicts: 1 of 2 (qa-explorer); 1 inconclusive, retrying', lastEvaluatedAt: ago(15_000),
    verify: {
      check: 'exploratory-qa', environment: 'staging', required: 2, allowedAgents: ['qa-explorer'], onInconclusive: 'retry',
      template: 'qa-explorer', passed: 1, failed: 0,
      verdicts: [
        { name: 'qa-r1-a1', verifier: 'agent qa-explorer for bob', result: 'pass', counted: true, run: 1, attempt: 1,
          summary: 'checkout, refunds and receipts work', evidenceURL: 'https://ci.example.com/qa/1', findings: 2, highFindings: 0,
          createdAt: ago(900_000),
          verifierActor: { kind: 'agent', principal: 'system:serviceaccount:agents:qa-explorer', agent: 'qa-explorer', sponsor: 'bob', text: 'agent qa-explorer for bob' } },
        { name: 'qa-r2-a1', verifier: 'agent qa-explorer for bob', result: 'inconclusive', counted: false, run: 2, attempt: 1,
          reason: 'inconclusive (retrying: attempt 2 of 2)', summary: 'the sandbox payment provider timed out', findings: 1, highFindings: 1,
          createdAt: ago(300_000),
          verifierActor: { kind: 'agent', principal: 'system:serviceaccount:agents:qa-explorer', agent: 'qa-explorer', sponsor: 'bob', text: 'agent qa-explorer for bob' } },
        { name: 'self', verifier: 'agent fixer for alice', result: 'pass', counted: false,
          reason: "not counted: the author's party (agent fixer for alice)", createdAt: ago(1_200_000),
          verifierActor: { kind: 'agent', agent: 'fixer', sponsor: 'alice', text: 'agent fixer for alice' } },
      ],
    },
  }
}

async function serveVerify(page: Page) {
  await page.route('**/api/v1/ui/gates**', async route => {
    const res = await route.fetch()
    await route.fulfill({ response: res, json: [...await res.json(), verifyGate()] })
  })
}

test.describe('Journey 026 — verdicts', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await serveVerify(page)
  })

  test('the stage drawer says what verification waits for, verdict by verdict', async ({ page }) => {
    await page.goto('/ui/#/p/default/payments-service/env/prod')
    const why = page.getByTestId('stage-drawer').locator('[data-section="why"]')
    await expect(why).toContainText('Needs 1 more passing verdict (1 of 2) for exploratory-qa in exploratory-qa.')
    await expect(why).toContainText('1 inconclusive, retrying')
    const meter = why.getByRole('meter', { name: 'Verdicts' })
    await expect(meter).toHaveAttribute('aria-valuetext', '1 of 2 verdicts for exploratory-qa; 1 inconclusive, retrying')
    const rows = why.getByRole('list', { name: 'Verdicts' }).getByRole('listitem')
    await expect(rows).toHaveCount(3)
    // The verifier is the ActorBadge: the agent and its sponsor, the kind in words.
    const pass = rows.nth(0)
    await expect(pass.locator('.k-actor')).toHaveAttribute('data-actor-kind', 'agent')
    await expect(pass.locator('.k-actor')).toHaveText('agent qa-explorer for bob')
    await expect(pass.locator('.k-chip')).toHaveText('✓Pass')
    await expect(pass.locator('.k-chip')).toHaveClass(/k-chip--verified/)
    await expect(pass.getByRole('link', { name: 'Evidence from qa-explorer (opens in a new tab)' }))
      .toHaveAttribute('href', 'https://ci.example.com/qa/1')
    // Inconclusive says what it does, and why it does not count yet.
    const inconclusive = rows.nth(1)
    await expect(inconclusive.locator('.k-chip')).toHaveText('?Inconclusive (retries)')
    await expect(inconclusive.locator('.k-chip')).toHaveClass(/k-chip--waiting/)
    await expect(inconclusive).toContainText('inconclusive (retrying: attempt 2 of 2)')
    await expect(inconclusive).toContainText('1 finding, 1 high')
    await expect(inconclusive.getByRole('link')).toHaveCount(0)
    await expect(rows.nth(2)).toContainText("not counted: the author's party")
    await expect(why.getByText(/kardinal verify payments-service staging --check exploratory-qa --pass/)).toBeVisible()
    await axe(page)
  })

  test('the Activity tab shows the same verdicts', async ({ page }) => {
    await page.goto('/ui/#/p/default/payments-service')
    await page.getByRole('tab', { name: /^Activity/ }).click()
    const panel = page.locator('.approval-quorum').filter({ has: page.getByRole('meter', { name: 'Verdicts' }) })
    await expect(panel.getByRole('list', { name: 'Verdicts' }).getByRole('listitem')).toHaveCount(3)
    await expect(panel.locator('.k-actor[data-actor-kind="agent"]').first()).toHaveText('agent qa-explorer for bob')
    await expect(panel.getByText('Inconclusive (retries)')).toBeVisible()
    await axe(page)
  })
})
