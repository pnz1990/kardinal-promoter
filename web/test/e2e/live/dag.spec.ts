// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-DAG-01: the station map (U2) with per-environment state, the gate on the
// edge into prod and its popover, and the PR in the stage drawer. Go test: TestUI_BrowserDAG (test/e2e/live/ui_browser_test.go).
//
// The Go test made Bundle KARDINAL_UI_BUNDLE in two namespaces: in
// KARDINAL_UI_NAMESPACE it is Verified in test and waits on its prod PR behind
// the passing gate KARDINAL_UI_GATE; in KARDINAL_UI_OTHER_NAMESPACE it failed
// in the only environment, solo.

import { test, expect, type Page } from '@playwright/test'
import { need, openPipeline, station, gateMark, stageDrawer, drawerHeading } from './live'

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

test('the station map shows each environment and gate with its state, the PR, and the drawer', async ({ page }) => {
  const reads = recordBundleReads(page)
  await openPipeline(page, base, ns)

  const testNode = station(page, 'test')
  await expect(testNode).toHaveAttribute('aria-label', /^test — Live, runs /)
  await expect(testNode).toHaveAttribute('data-tone', 'verified')

  const prodNode = station(page, 'prod')
  await expect(prodNode).toHaveAttribute('aria-label', /^prod — Waiting for PR merge, /)
  await expect(prodNode).toHaveAttribute('data-tone', 'waiting')
  await expect(prodNode).toContainText(`#${prNumber}`)

  // The gate sits on the edge into prod, passing.
  const gateNode = gateMark(page, gate)
  await expect(gateNode).toHaveAttribute('aria-label', `gate ${gate}, Passing`)
  await expect(gateNode).toHaveAttribute('data-tone', 'verified')

  // Only this namespace's Bundle: no solo station from the other namespace.
  await expect(page.locator('.station-map [data-station]')).toHaveCount(2)

  // The gate's popover names it, its state and its expression.
  const popover = page.locator('.gate-popover')
  await gateNode.hover()
  await expect(popover).toBeVisible()
  await expect(popover).toHaveAttribute('role', 'tooltip')
  await expect(popover).toContainText(gate)
  await expect(popover).toContainText('Passing')
  await expect(popover).toContainText('true') // the expression
  await expect(popover).toContainText(/evaluated /)
  await page.mouse.move(0, 0)

  // prod's stage drawer links its PR.
  await prodNode.click()
  const detail = stageDrawer(page)
  await expect(drawerHeading(page)).toHaveText('prod')
  await expect(detail.locator(`a[href="${prURL}"]`).first()).toBeVisible()
  await detail.getByRole('button', { name: 'Close prod' }).click()

  // Picking the Bundle in the timeline reads its graph again.
  const reread = page.waitForRequest(r => new URL(r.url()).pathname.endsWith(`/bundles/${bundle}/graph`))
  await page.locator(`button.bundle-chip[title^="${bundle}:"]`).click()
  await reread
  await expect(page.locator('.station-map [data-station]')).toHaveCount(2)
  expectScoped(reads, ns)
})

test('the same Bundle name in another namespace shows that namespace’s map', async ({ page }) => {
  const reads = recordBundleReads(page)
  await openPipeline(page, base, other)
  const solo = station(page, 'solo')
  await expect(solo).toHaveAttribute('aria-label', /^solo — Failed/)
  await expect(solo).toHaveAttribute('data-tone', 'failed')
  await expect(page.locator('.station-map [data-station]')).toHaveCount(1)
  // Past one 5 s poll: still the other namespace's graph.
  await page.waitForRequest(r => new URL(r.url()).pathname.endsWith(`/bundles/${bundle}/graph`))
  await expect(page.locator('.station-map [data-station]')).toHaveCount(1)
  await expect(solo).toBeVisible()
  expectScoped(reads, other)
})

// UI-TABLE-01, UI-MATRIX-01: the Map tab's Table and Matrix views of the same pipeline.
test('the Table lists each environment and the Matrix each version by environment', async ({ page }) => {
  await openPipeline(page, base, ns)
  const views = page.getByRole('radiogroup', { name: 'View' })
  await views.getByRole('radio', { name: 'View: Table' }).click()
  const table = page.getByRole('region', { name: 'Environments' })
  await expect(table.getByRole('row', { name: /^test/ })).toContainText('Live')
  const prodRow = table.getByRole('row', { name: /^prod/ })
  await expect(prodRow).toContainText('Waiting for PR merge')
  await expect(prodRow.getByRole('link', { name: `PR #${prNumber}` })).toHaveAttribute('href', prURL)
  await expect(prodRow).toContainText(`${gate}: Passing`)

  await views.getByRole('radio', { name: 'View: Matrix' }).click()
  const matrix = page.getByRole('region', { name: 'Versions by environment' })
  const row = matrix.locator(`tr[data-bundle="${bundle}"]`)
  await expect(row).toHaveAttribute('data-followed', 'true')
  await expect(row.locator('td[aria-label^="test:"]')).toHaveAttribute('aria-label', 'test: Live')
  await expect(row.locator('td[aria-label^="prod:"]')).toHaveAttribute('aria-label', 'prod: Waiting for PR merge')
})
