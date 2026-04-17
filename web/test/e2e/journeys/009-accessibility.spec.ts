// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 009: WCAG 2.1 AA automated accessibility check (#748).

import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

// #761: color-contrast rule enabled after full color system audit (#757, #760).
// #762: nested-interactive rule enabled after PipelineLaneView redesign (#758) and
//       DAG SVG role="link" removal from PR badge text element (#762).
const DISABLED_RULES: string[] = []

test.describe('Journey 009 — WCAG 2.1 AA accessibility', () => {
  test('default dashboard state passes structural WCAG 2.1 AA checks', async ({ page }) => {
    await page.goto('/')
    await page.waitForSelector('text=kardinal-test-app', { timeout: 8000 })

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa'])
      .disableRules(DISABLED_RULES)
      .analyze()

    if (results.violations.length > 0) {
      const summary = results.violations.map(v =>
        `  [${v.impact}] ${v.id}: ${v.description}\n` +
        v.nodes.slice(0, 2).map(n => `    → ${n.html.slice(0, 200)}\n      target: ${n.target[0]}`).join('\n'),
      ).join('\n')
      throw new Error(`${results.violations.length} WCAG 2.1 AA structural violation(s):\n${summary}`)
    }

    expect(results.violations).toHaveLength(0)
  })

  test('pipeline-selected state passes structural WCAG 2.1 AA checks', async ({ page }) => {
    await page.goto('/')
    await page.waitForSelector('text=kardinal-test-app', { timeout: 8000 })
    await page.getByText('kardinal-test-app').first().click()
    await page.waitForTimeout(500)

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa'])
      .disableRules(DISABLED_RULES)
      .analyze()

    if (results.violations.length > 0) {
      const summary = results.violations.map(v =>
        '  [' + v.impact + '] ' + v.id + ': ' + v.description + '\n' +
        v.nodes.slice(0, 3).map(n => '    -> ' + n.html.slice(0, 200) + '\n      target: ' + n.target[0]).join('\n'),
      ).join('\n')
      throw new Error(results.violations.length + ' WCAG 2.1 AA structural violation(s) in pipeline view:\n' + summary)
    }

    expect(results.violations).toHaveLength(0)
  })
})
