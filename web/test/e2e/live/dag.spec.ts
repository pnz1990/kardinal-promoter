// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-DAG-01: the promotion DAG with per-environment state, the PR badge and
// the tooltip. Go test: TestUI_BrowserDAG (test/e2e/live/ui_browser_test.go).
//
// The Go test made Bundle KARDINAL_UI_BUNDLE in two namespaces: in
// KARDINAL_UI_NAMESPACE it is Verified in test and waits on its prod PR behind
// the passing gate KARDINAL_UI_GATE; in KARDINAL_UI_OTHER_NAMESPACE it failed
// in the only environment, solo.

import { test, expect, type Page } from '@playwright/test'
import { need, openPipeline, dagNode } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const other = need('KARDINAL_UI_OTHER_NAMESPACE')
const bundle = need('KARDINAL_UI_BUNDLE')
const gate = need('KARDINAL_UI_GATE')
const prURL = need('KARDINAL_UI_PR_URL')
const prNumber = need('KARDINAL_UI_PR_NUMBER')

/** recordBundleReads collects the page's graph and steps requests. */
function recordBundleReads(page: Page): URL[] {
  const reads: URL[] = []
  page.on('request', r => {
    const u = new URL(r.url())
    if (/^\/api\/v1\/ui\/bundles\/[^/]+\/(graph|steps)$/.test(u.pathname)) reads.push(u)
  })
  return reads
}

/** expectScoped requires reads, all of them for bundle in namespace. */
function expectScoped(reads: URL[], namespace: string) {
  expect(reads.length).toBeGreaterThan(0)
  for (const u of reads) {
    expect(u.pathname, 'the Bundle on screen').toMatch(new RegExp(`/bundles/${bundle}/(graph|steps)$`))
    expect(u.searchParams.get('namespace'), `${u.pathname}${u.search} names the namespace on screen`).toBe(namespace)
  }
}

test('the DAG shows each environment and gate with its state, the PR badge and tooltips', async ({ page }) => {
  const reads = recordBundleReads(page)
  await openPipeline(page, base, ns)

  const testNode = dagNode(page, 'test')
  await expect(testNode).toHaveAttribute('aria-label', 'test — Verified')
  await expect(testNode).toHaveAttribute('data-health-state', 'Ready')
  await expect(testNode).toHaveClass(/\bdag-node--ready\b/)
  await expect(testNode).not.toContainText('🔗')

  const prodNode = dagNode(page, 'prod')
  await expect(prodNode).toHaveAttribute('aria-label', 'prod — WaitingForMerge')
  await expect(prodNode).toHaveAttribute('data-health-state', 'Reconciling')
  await expect(prodNode).toContainText(`🔗 #${prNumber}`)
  await expect(prodNode).toContainText('⏱ ')

  const gateNode = dagNode(page, gate)
  await expect(gateNode).toHaveAttribute('aria-label', `${gate} — Pass`)
  await expect(gateNode).toHaveAttribute('data-health-state', 'Ready')
  await expect(gateNode).toContainText(`🔒 ${gate}`)

  // Only this namespace's Bundle: no solo node from the other namespace.
  await expect(page.locator('g.dag-node')).toHaveCount(3)
  await expect(page.getByTestId('dag-static-banner')).toHaveCount(0)

  const tooltip = page.getByTestId('dag-tooltip')
  await prodNode.hover()
  await expect(tooltip).toBeVisible()
  await expect(tooltip).toHaveAttribute('role', 'tooltip')
  await expect(tooltip).toContainText('prod')
  await expect(tooltip).toContainText('State:')
  await expect(tooltip).toContainText('WaitingForMerge')
  await expect(tooltip.getByRole('link', { name: 'View Pull Request ↗' })).toHaveAttribute('href', prURL)

  await gateNode.hover()
  await expect(tooltip).toContainText(gate)
  await expect(tooltip).toContainText('Pass')
  await expect(tooltip).toContainText('true') // the expression
  await expect(tooltip).toContainText(/Evaluated /)
  await expect(tooltip.getByRole('link')).toHaveCount(0)

  // Picking the Bundle in the timeline reads its graph again.
  const reread = page.waitForRequest(r => new URL(r.url()).pathname.endsWith(`/bundles/${bundle}/graph`))
  await page.locator(`button.bundle-chip[title^="${bundle}:"]`).click()
  await reread
  await expect(page.locator('g.dag-node')).toHaveCount(3)
  expectScoped(reads, ns)
})

test('the same Bundle name in another namespace shows that namespace’s DAG', async ({ page }) => {
  const reads = recordBundleReads(page)
  await openPipeline(page, base, other)
  const solo = dagNode(page, 'solo')
  await expect(solo).toHaveAttribute('aria-label', 'solo — Failed')
  await expect(solo).toHaveAttribute('data-health-state', 'Error')
  await expect(page.locator('g.dag-node')).toHaveCount(1)
  // Past one 5 s poll: still the other namespace's graph.
  await page.waitForRequest(r => new URL(r.url()).pathname.endsWith(`/bundles/${bundle}/graph`))
  await expect(page.locator('g.dag-node')).toHaveCount(1)
  await expect(solo).toBeVisible()
  expectScoped(reads, other)
})
