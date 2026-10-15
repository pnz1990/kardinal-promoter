// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-FREEZE-01: a freeze's banner and the stage drawer of a step it holds,
// with the UI's freeze controls off. Go test: TestUI_BrowserFreezes
// (test/e2e/live/ui_browser_test.go).
//
// KARDINAL_UI_FROZEN_BY stopped the line in KARDINAL_UI_NAMESPACE for two
// hours (freeze KARDINAL_UI_FREEZE, reason KARDINAL_UI_REASON); podinfo's
// version is held at test. The main UI runs without TokenReview, so its
// freeze controls are off. Other tests' freezes may be listed too: the spec
// reads its own by name.

import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { need, pipelineHash, stageDrawer } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const name = need('KARDINAL_UI_FREEZE')
const by = need('KARDINAL_UI_FROZEN_BY')
const reason = need('KARDINAL_UI_REASON')

async function axe(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(results.violations.map(v => `${v.id}: ${v.nodes.map(n => n.target).join(' ')}`)).toEqual([])
}

test('the banner says who stopped the line, where, until when and why; the drawer that it holds the step', async ({ page }) => {
  await page.goto(`${base}/ui/${pipelineHash(ns)}/env/test`)
  const banner = page.getByRole('alert', { name: `Freeze ${name}` })
  await expect(banner).toContainText(`The line is stopped for everyone in ${ns} by`)
  await expect(banner.locator('.k-actor')).toContainText(by.replace(/^system:serviceaccount:/, '').replace(':', '/'))
  await expect(banner).toContainText(/since (just now|\d+m ago), ends in 1h \d+m/)
  await expect(banner).toContainText(`: ${reason}`)
  await expect(banner).toHaveClass(/k-banner--failed/)
  // The controls are off: the command, with the controller's reason, and no Freeze menu.
  await expect(banner.getByText(`kardinal unfreeze ${name}`)).toHaveAttribute('title', /ui\.freezeControls/)
  await expect(banner.getByRole('button', { name: 'Unfreeze' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Freeze', exact: true })).toHaveCount(0)
  const why = stageDrawer(page).locator('[data-section="why"]')
  await expect(why).toContainText('Held by a freeze: it promotes once the freeze is lifted, or exempted here.')
  await expect(why).toContainText(`frozen by ${name}`)
  await axe(page)
})
