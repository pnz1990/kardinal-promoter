// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-SHELL-01: the keyboard shortcuts, the dark and light themes, the deep
// links and browser history, and the polling with its "refreshed ago"
// indicator. Go test: TestUI_BrowserShell (test/e2e/live/ui_browser_test.go).
//
// In KARDINAL_UI_NAMESPACE, podinfo has one environment, test, and one
// Bundle, Verified there by step KARDINAL_UI_TEST_STEP. Pipeline
// KARDINAL_UI_IDLE_PIPELINE has no Bundle; the polling test pauses it
// through the API, and the Go test checks it is paused.

import { test, expect, type Page, type Request, type Route } from '@playwright/test'
import { need, openPipeline, pipelineHash, filterSidebar, sidebarRows, sidebarRow, dagNode, PIPELINE } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const testStep = need('KARDINAL_UI_TEST_STEP')
const idle = need('KARDINAL_UI_IDLE_PIPELINE')

const PIPELINES_PATH = '/api/v1/ui/pipelines'
const isPipelinesPoll = (url: URL | string) => new URL(url.toString()).pathname === PIPELINES_PATH

const filter = (page: Page) => page.getByRole('textbox', { name: 'Filter pipelines by name or namespace' })
const shortcuts = (page: Page) => page.getByRole('dialog', { name: 'Keyboard shortcuts' })
const details = (page: Page) => page.getByTestId('node-detail')
const hashParams = (page: Page) => new URLSearchParams(new URL(page.url()).hash.slice(1))
const heading = (page: Page) => page.getByRole('heading', { level: 1 })
/** The "refreshed ago" indicator inside the Refresh data button. */
const indicator = (page: Page) => page.getByRole('button', { name: 'Refresh data' }).locator('span[aria-live="polite"]')

/** The UI polls every 5 s; two requests closer than this were not both polls. */
const BELOW_POLL_MS = 4_000

/**
 * expectTriggersPoll runs act right after a pipelines request and checks
 * that act sends the next one, well before the next 5 s poll would.
 */
async function expectTriggersPoll(page: Page, act: () => Promise<void>) {
  await page.waitForRequest(r => isPipelinesPoll(r.url()))
  const since = Date.now()
  const next = page.waitForRequest(r => isPipelinesPoll(r.url()))
  await act()
  await next
  expect(Date.now() - since).toBeLessThan(BELOW_POLL_MS)
}

/** expectFresh checks the indicator right after a successful poll. */
async function expectFresh(page: Page) {
  await expect(indicator(page)).toHaveAttribute('aria-label', 'Data just now')
  await expect(indicator(page)).toHaveText('● just now')
  await expect(indicator(page)).toHaveAttribute('title', 'Last updated')
}

/** secondsAgo is the age the indicator shows ("Data 12s ago"), or -1. */
async function secondsAgo(page: Page): Promise<number> {
  const label = await indicator(page).getAttribute('aria-label')
  const m = label?.match(/^Data (\d+)s ago$/)
  return m ? Number(m[1]) : -1
}

test('keyboard shortcuts: / focuses the filter, ? shows the help, r refreshes, Esc closes the open panel', async ({ page }) => {
  await openPipeline(page, base, ns)

  // "/" focuses the filter; in the field, keys are typed, not shortcuts.
  await page.keyboard.press('/')
  await expect(filter(page)).toBeFocused()
  await page.keyboard.type(`${ns}?`)
  await expect(filter(page)).toHaveValue(`${ns}?`)
  await expect(shortcuts(page)).toHaveCount(0)
  await page.keyboard.press('Backspace')
  await expect(sidebarRows(page)).toHaveCount(2)
  // Esc in the field clears it and leaves it.
  await page.keyboard.press('Escape')
  await expect(filter(page)).toHaveValue('')
  await expect(filter(page)).not.toBeFocused()

  // "?" opens the help with focus inside; "?", Esc, the close button and a
  // click outside it close it.
  await page.keyboard.press('?')
  const help = shortcuts(page)
  await expect(help).toHaveAttribute('aria-modal', 'true')
  const rows = help.getByRole('row')
  const want: [string, string][] = [
    ['/', 'Focus pipeline search'],
    ['?', 'Show / hide this help panel'],
    ['r', 'Refresh data now'],
    ['Esc', 'Close the open side panel'],
  ]
  await expect(rows).toHaveCount(want.length)
  for (const [i, [key, description]] of want.entries()) {
    await expect(rows.nth(i).getByRole('cell').nth(0)).toHaveText(key)
    await expect(rows.nth(i).getByRole('cell').nth(1)).toHaveText(description)
  }
  const close = help.getByRole('button', { name: 'Close keyboard shortcuts' })
  await expect(close).toBeFocused()
  await page.keyboard.press('?')
  await expect(help).toHaveCount(0)
  await page.keyboard.press('?')
  await expect(help).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(help).toHaveCount(0)
  await page.keyboard.press('?')
  await close.click()
  await expect(help).toHaveCount(0)
  await page.keyboard.press('?')
  await help.click({ position: { x: 5, y: 5 } })
  await expect(help).toHaveCount(0)

  // Esc closes the node details, and the link loses the node.
  await dagNode(page, 'test').click()
  await expect(details(page)).toContainText('Environment: test')
  expect(hashParams(page).get('node')).toBe(testStep)
  await page.keyboard.press('Escape')
  await expect(details(page)).toHaveCount(0)
  await expect.poll(() => hashParams(page).get('node')).toBeNull()

  // "r" fetches the data at once, without waiting for the next poll.
  await page.locator('body').click({ position: { x: 1, y: 1 } })
  await expectTriggersPoll(page, () => page.keyboard.press('r'))
})

test('the theme follows the system until the user picks one, which the next visit keeps', async ({ page }) => {
  const root = page.locator('#root > div').first()
  const toggle = (name: string) => page.getByRole('button', { name })
  const stored = () => page.evaluate(() => localStorage.getItem('kardinal-theme'))
  const dark = 'rgb(15, 23, 42)'
  const light = 'rgb(241, 245, 249)'

  await page.emulateMedia({ colorScheme: 'dark' })
  await openPipeline(page, base, ns)
  await expect(page.locator('html')).not.toHaveAttribute('data-theme')
  await expect(root).toHaveCSS('background-color', dark)
  await expect(toggle('Switch to light mode')).toHaveText('☀')
  expect(await stored()).toBeNull()

  // No choice yet: the system's setting, as it changes.
  await page.emulateMedia({ colorScheme: 'light' })
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  await expect(root).toHaveCSS('background-color', light)
  await expect(toggle('Switch to dark mode')).toHaveText('☾')
  await page.emulateMedia({ colorScheme: 'dark' })
  await expect(page.locator('html')).not.toHaveAttribute('data-theme')
  expect(await stored()).toBeNull()

  // The user's choice wins over the system's and is kept across visits.
  await toggle('Switch to light mode').click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  await expect(root).toHaveCSS('background-color', light)
  expect(await stored()).toBe('light')
  await page.reload()
  await expect(heading(page)).toHaveText(PIPELINE)
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  await expect(toggle('Switch to dark mode')).toBeVisible()

  await toggle('Switch to dark mode').click()
  await expect(page.locator('html')).not.toHaveAttribute('data-theme')
  await expect(root).toHaveCSS('background-color', dark)
  expect(await stored()).toBe('dark')
  await page.emulateMedia({ colorScheme: 'light' })
  await expect(toggle('Switch to light mode')).toBeVisible()
  await expect(page.locator('html')).not.toHaveAttribute('data-theme')
})

test('a link opens the pipeline and node it names, and Back and Forward walk the selections', async ({ page }) => {
  await openPipeline(page, base, ns, PIPELINE, { node: testStep })
  await expect(details(page)).toContainText('Environment: test')
  await expect(dagNode(page, 'test')).toHaveAttribute('aria-pressed', 'true')

  // Closing the details is one history entry.
  await details(page).getByRole('button', { name: 'Close' }).click()
  await expect(details(page)).toHaveCount(0)
  await expect.poll(() => hashParams(page).get('node')).toBeNull()
  await page.goBack()
  await expect(details(page)).toContainText('Environment: test')
  expect(hashParams(page).get('node')).toBe(testStep)
  await page.goForward()
  await expect.poll(() => hashParams(page).get('node')).toBeNull()
  await expect(details(page)).toHaveCount(0)

  // So is switching pipelines in the sidebar.
  await filterSidebar(page, ns)
  await sidebarRow(page, idle).click()
  await expect(heading(page)).toHaveText(idle)
  expect(hashParams(page).get('pipeline')).toBe(idle)
  expect(hashParams(page).get('ns')).toBe(ns)
  // No Bundle: the DAG is the pipeline's environments, not started.
  await expect(dagNode(page, 'test')).toHaveAttribute('aria-label', 'test — NotStarted')
  await page.goBack()
  await expect(heading(page)).toHaveText(PIPELINE)
  await expect(dagNode(page, 'test')).toHaveAttribute('aria-label', 'test — Verified')
  await expect(details(page)).toHaveCount(0)
  await page.goForward()
  await expect(heading(page)).toHaveText(idle)

  // A link to the other pipeline opens it directly.
  await page.goto(`${base}/ui/${pipelineHash(ns, idle)}`)
  await expect(heading(page)).toHaveText(idle)
})

test('the data refreshes every 5 s, and the indicator tells how old it is and why', async ({ page }) => {
  await openPipeline(page, base, ns)
  await expectFresh(page)

  // A change made elsewhere shows without a reload.
  await filterSidebar(page, ns)
  const pausedBadge = sidebarRow(page, idle).getByTitle('Pipeline is paused — no new promotions will start')
  await expect(pausedBadge).toHaveCount(0)
  const status = await page.evaluate(async ([pipeline, namespace]) => {
    const resp = await fetch('/api/v1/ui/pause', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ pipeline, namespace }),
    })
    return resp.status
  }, [idle, ns])
  expect(status).toBe(200)
  await expect(pausedBadge).toHaveText('PAUSED', { timeout: 10_000 })

  // An API that never answers: the read gives up after 10 s and says so,
  // and the polling goes on.
  // The sidebar shows the error above the last list it read, with the filter
  // as it was; the open pipeline stays.
  const noAnswer = `Error: GET ${PIPELINES_PATH} got no answer within 10 s. The UI tries again on the next refresh.`
  const held: Request[] = []
  const hold = (route: Route) => { held.push(route.request()) }
  await page.route(isPipelinesPoll, hold)
  await expect(indicator(page)).toHaveAttribute('title', noAnswer, { timeout: 20_000 })
  await expect(indicator(page)).toHaveText(/^⚠ \d+s ago$/)
  await expect(page.getByText(noAnswer, { exact: true })).toBeVisible()
  await expect(pausedBadge).toHaveText('PAUSED')
  await expect(page.getByRole('textbox', { name: 'Filter pipelines by name or namespace' })).toHaveValue(ns)
  await expect.poll(() => held.length, { timeout: 20_000 }).toBeGreaterThanOrEqual(2)
  await expect(heading(page)).toHaveText(PIPELINE)
  await page.unroute(isPipelinesPoll, hold)
  await expectFresh(page)
  await expect(page.getByText(noAnswer, { exact: true })).toHaveCount(0)
  await expect(pausedBadge).toHaveText('PAUSED')

  // An API that fails: the error and the growing age; a click retries at once.
  const fail = (route: Route) => route.fulfill({ status: 503, contentType: 'text/plain', body: 'unavailable' })
  await page.route(isPipelinesPoll, fail)
  await expect(indicator(page)).toHaveAttribute('title', /^Error: API error 503/)
  await expect(page.getByText(/^Error: API error 503/)).toBeVisible()
  await expect(pausedBadge).toHaveText('PAUSED')
  await expect(indicator(page)).toHaveText(/^⚠ (just now|\d+s ago)$/)
  await expect.poll(() => secondsAgo(page), { timeout: 15_000 }).toBeGreaterThanOrEqual(6)
  const refresh = page.getByRole('button', { name: 'Refresh data' })
  await expectTriggersPoll(page, () => refresh.click())
  await expect(indicator(page)).toHaveAttribute('title', /^Error: API error 503/)

  await page.unroute(isPipelinesPoll, fail)
  await refresh.click()
  await expectFresh(page)
})
