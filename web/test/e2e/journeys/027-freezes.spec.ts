// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 027 (#1908): freezes (docs/agentic/freezes.md). The journey serves
// GET /freezes: alice froze agents in every namespace, and the pager bot
// stopped the line in payments for 2 hours. Each freeze is a Banner on every
// view: whom it holds and where, who froze (the actor badge), since when and
// until when, and why. With the UI's freeze controls on (Helm
// ui.freezeControls) the caller lifts one after a confirmation, and the
// Freeze menu freezes agents with a reason; with them off the banner names
// kardinal unfreeze with the controller's reason and the menu is gone. A step
// a freeze holds says so in the stage drawer. A banner that arrives after the
// view does not shift it beyond MAX_SHIFT. Each surface passes axe in both
// themes.

import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

async function axe(page: Page) {
  // No transitions: axe must read the theme's colours, not a fade between them.
  await page.addStyleTag({ content: '*, *::before, *::after { transition: none !important; }' })
  for (const scheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: scheme })
    await page.evaluate(t => document.documentElement.setAttribute('data-theme', t), scheme)
    const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
    expect(results.violations.map(v => `${scheme} ${v.id}: ${v.nodes.map(n => n.target).join(' ')}`)).toEqual([])
  }
}

const ago = (ms: number) => new Date(Date.now() - ms).toISOString()
const inFuture = (ms: number) => new Date(Date.now() + ms).toISOString()

const AGENTS = 'kardinal-freeze-agents'
const STOP = 'kardinal-freeze-everyone-payments'
const CONTROLS_OFF = "the UI's freeze controls are off (Helm ui.freezeControls); use kardinal freeze and kardinal unfreeze"

function freezes() {
  return [
    { name: AGENTS, actors: 'agents', namespaces: [], scope: 'agents, every namespace', active: true, since: ago(3 * 3_600_000),
      createdBy: 'alice', reason: 'INC-4211: agents paused while we investigate', exemptions: 0 },
    { name: STOP, actors: 'everyone', namespaces: ['payments'], scope: 'everyone, namespaces payments', active: true, since: ago(600_000),
      until: inFuture(2 * 3_600_000 + 30_000), createdBy: 'system:serviceaccount:ops:pager', reason: 'INC-4212 stop', exemptions: 1 },
    { name: 'kardinal-freeze-agents-old', actors: 'agents', namespaces: ['default'], scope: 'agents, namespaces default', active: false,
      since: ago(86_400_000), until: ago(3_600_000), createdBy: 'bob', reason: 'over', exemptions: 0 },
  ]
}

/** Serves the freezes; with controls off, the access check of changewindows/freeze says no (ui_access.go). */
async function serveFreezes(page: Page, { controls = true, delayMs = 0 } = {}) {
  const state = { list: freezes(), writes: [] as Array<{ path: string; body: unknown }> }
  await page.route('**/api/v1/ui/freezes', async route => {
    if (delayMs) await new Promise(r => setTimeout(r, delayMs))
    await route.fulfill({ json: { freezes: state.list } })
  })
  await page.route(/\/api\/v1\/ui\/(un)?freeze$/, async route => {
    const path = new URL(route.request().url()).pathname
    const body = route.request().postDataJSON()
    state.writes.push({ path, body })
    if (path.endsWith('/unfreeze')) state.list = state.list.filter(f => f.name !== body.name)
    await route.fulfill({ json: { message: path.endsWith('/unfreeze') ? `lifted freeze ${body.name}` : 'frozen' } })
  })
  if (!controls) {
    await page.route('**/api/v1/ui/access', async route => {
      const res = await route.fetch()
      const body = await res.json()
      const checks = route.request().postDataJSON().checks as Array<{ resource: string }>
      body.results = body.results.map((r: object, i: number) => checks[i].resource === 'changewindows'
        ? { allowed: false, reason: CONTROLS_OFF } : r)
      await route.fulfill({ response: res, json: body })
    })
  }
  return state
}

/** Starts recording layout shifts before the page's own scripts run (journey 024). */
async function recordShifts(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as { __shift: number }
    w.__shift = 0
    new PerformanceObserver(list => {
      for (const e of list.getEntries() as Array<PerformanceEntry & { value: number; hadRecentInput: boolean }>) {
        if (!e.hadRecentInput) w.__shift += e.value
      }
    }).observe({ type: 'layout-shift', buffered: true })
  })
}

/** What journey 024 allows: a data banner arriving with its data. */
const MAX_SHIFT = 0.05

test.describe('Journey 027 — freezes', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
  })

  test('every view shows each active freeze: whom, where, who, since, until and why', async ({ page }) => {
    await serveFreezes(page)
    await page.goto('/ui/')
    const agents = page.getByRole('alert', { name: `Freeze ${AGENTS}` })
    await expect(agents).toContainText('Agents are frozen in every namespace by alice, since 3h ago, until it is lifted: INC-4211: agents paused while we investigate')
    await expect(agents).toHaveClass(/k-banner--blocked/)
    await expect(agents.locator('.k-actor')).toHaveAttribute('data-actor-kind', 'human')
    const stop = page.getByRole('alert', { name: `Freeze ${STOP}` })
    await expect(stop).toContainText('The line is stopped for everyone in payments by')
    await expect(stop).toContainText(/since 10m ago, ends in 2h( \d+m)?: INC-4212 stop \(1 exemption in effect\)/)
    await expect(stop).toHaveClass(/k-banner--failed/)
    await expect(stop.locator('.k-actor')).toHaveAttribute('data-actor-kind', 'bot')
    await expect(stop.locator('.k-actor__name')).toHaveText('ops/pager')
    // An ended freeze is not shown.
    await expect(page.getByRole('alert', { name: 'Freeze kardinal-freeze-agents-old' })).toHaveCount(0)
    await axe(page)
    // The same banners head a pipeline's page.
    await page.goto('/ui/#/p/default/payments-service')
    await expect(page.locator('.station-map')).toBeVisible()
    await expect(page.getByTestId('freeze-banner').getByRole('alert')).toHaveCount(2)
  })

  test('with the freeze controls on: Unfreeze asks, then lifts it', async ({ page }) => {
    const state = await serveFreezes(page)
    await page.goto('/ui/')
    const agents = page.getByRole('alert', { name: `Freeze ${AGENTS}` })
    await agents.getByRole('button', { name: 'Unfreeze' }).click()
    const dialog = page.getByRole('dialog', { name: `Lift ${AGENTS}?` })
    await expect(dialog).toBeVisible()
    await axe(page)
    await dialog.getByRole('button', { name: 'Unfreeze' }).click()
    await expect(agents).toHaveCount(0)
    expect(state.writes).toEqual([{ path: '/api/v1/ui/unfreeze', body: { name: AGENTS } }])
    await expect(page.getByRole('alert', { name: `Freeze ${STOP}` })).toBeVisible()
  })

  test('with the freeze controls on: Freeze agents needs a reason', async ({ page }) => {
    const state = await serveFreezes(page)
    await page.goto('/ui/')
    await page.getByRole('button', { name: 'Freeze', exact: true }).click()
    await page.getByRole('button', { name: 'Freeze agents…' }).click()
    const dialog = page.getByRole('dialog', { name: 'Freeze agents?' })
    await dialog.getByLabel('Reason (required)').fill('INC-4213 agents misbehave')
    await dialog.getByLabel('Ends').selectOption('60')
    await axe(page)
    await dialog.getByRole('button', { name: 'Freeze agents' }).click()
    await expect(dialog).toHaveCount(0)
    expect(state.writes).toEqual([{ path: '/api/v1/ui/freeze', body: { actors: 'agents', reason: 'INC-4213 agents misbehave', minutes: 60 } }])
  })

  test('with the freeze controls off: the command, with why, and no Freeze menu', async ({ page }) => {
    const state = await serveFreezes(page, { controls: false })
    await page.goto('/ui/')
    const agents = page.getByRole('alert', { name: `Freeze ${AGENTS}` })
    await expect(agents).toBeVisible()
    const cmd = agents.getByText(`kardinal unfreeze ${AGENTS}`)
    await expect(cmd).toHaveAttribute('title', CONTROLS_OFF)
    await expect(page.getByRole('button', { name: 'Unfreeze' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Freeze', exact: true })).toHaveCount(0)
    await axe(page)
    expect(state.writes).toEqual([])
  })

  test('the stage drawer says a freeze holds the step, in the controller\'s words', async ({ page }) => {
    await serveFreezes(page)
    const msg = `frozen by ${AGENTS} (agents, every namespace) since 2026-10-15T06:00:00Z by alice: INC-4211 — lift it with: kardinal unfreeze ${AGENTS}, or let this through with: kardinal override kardinal-test-app uat --freeze --reason <why>`
    await page.route('**/api/v1/ui/bundles/kardinal-test-app-abc123/steps**', async route => {
      const res = await route.fetch()
      const steps = await res.json()
      await route.fulfill({ response: res, json: [...steps, { name: 'step-uat-abc', namespace: 'default', pipeline: 'kardinal-test-app',
        bundle: 'kardinal-test-app-abc123', environment: 'uat', stepType: 'kustomize-set-image', state: 'Pending', message: msg }] })
    })
    await page.goto('/ui/#/p/default/kardinal-test-app/env/uat')
    const why = page.getByTestId('stage-drawer').locator('[data-section="why"]')
    await expect(why).toContainText('Held by a freeze: it promotes once the freeze is lifted, or exempted here.')
    await expect(why).toContainText(`kardinal unfreeze ${AGENTS}`)
    await axe(page)
  })

  test('a freeze banner that arrives after the view does not shift it', async ({ page }) => {
    await recordShifts(page)
    await serveFreezes(page, { delayMs: 800 })
    await page.goto('/ui/#/p/default/payments-service/env/prod')
    await expect(page.getByTestId('stage-drawer')).toBeVisible()
    await expect(page.getByTestId('freeze-banner')).toBeVisible()
    await page.waitForTimeout(1500)
    expect(await page.evaluate(() => (window as unknown as { __shift: number }).__shift)).toBeLessThan(MAX_SHIFT)
  })
})
