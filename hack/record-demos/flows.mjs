// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// flows.mjs — the scripted clips. Each flow starts at a route of the UI, runs against
// the fixtures (fixtures.mjs) and marks its poster frame. A step the UI does not have
// yet (the station map's zoom and minimap, the stage drawer) is skipped when its
// control is absent, so the harness keeps running across UI changes; re-record when
// they land.

/** True when locator matches something visible within a short wait. */
async function present(locator, ms = 1500) {
  try {
    await locator.first().waitFor({ state: 'visible', timeout: ms })
    return true
  } catch {
    return false
  }
}

/** The map's node or station for an environment or gate, whichever the UI draws. */
const stage = (page, name) => page.locator(`[aria-label^="${name} — "]`).first()

export const FLOWS = [
  {
    name: 'first-look',
    title: 'From the fleet to a pipeline, following a version',
    docs: ['index.md', 'quickstart.md'],
    themes: ['light', 'dark'],
    start: '#/',
    async run(d, page) {
      await d.pause(1400)
      await d.move(page.getByRole('button', { name: 'checkout', exact: true }))
      await d.pause(500)
      await d.click(page.getByRole('button', { name: 'checkout', exact: true }), { after: 1600 })
      d.poster()
      await d.move(stage(page, 'uat'))
      await d.pause(700)
      await d.move(stage(page, 'prod-eu'))
      await d.pause(900)
      // Follow the version that runs in prod today, then come back to the new one.
      await d.click(page.locator('.bundle-chip', { hasText: '1-7-2' }), { after: 1700 })
      await d.click(page.locator('.bundle-chip', { hasText: '1-8-0' }), { after: 1400 })
    },
  },
  {
    name: 'why-waiting',
    title: 'Why a stage is waiting: the gate, its rule and its reason',
    docs: ['installation.md', 'policy-gates.md'],
    themes: ['light'],
    start: '#/p/shop/checkout',
    async run(d, page) {
      await d.pause(1200)
      await d.click(stage(page, 'prod-us'), { after: 1500 })
      d.poster()
      await d.pause(900)
      await d.click(stage(page, 'uat-soak'), { after: 1800 })
      // The stage drawer (U3) lists why it waits; scroll it into view when it is there.
      const why = page.getByText(/soaked 12m of 30m/).first()
      if (await present(why)) await d.move(why)
      await d.pause(1400)
    },
  },
  {
    name: 'approve-override',
    title: 'Approve a release, and confirm before overriding or rejecting a gate',
    docs: ['policy-gates.md'],
    themes: ['light'],
    start: '#/p/shop/checkout?tab=activity',
    async run(d, page) {
      await d.pause(1300)
      const row = page.locator('.approval-actions').first()
      await d.move(page.getByText('canary error rate flat').first())
      await d.pause(800)
      d.poster()
      await d.click(row.getByRole('button', { name: 'Approve' }), { after: 1800 })
      // A second gate decision asks first: Reject (or Override, where the UI offers it).
      const override = page.getByRole('button', { name: /^Override/ })
      const reject = page.getByRole('button', { name: 'Reject' })
      const ask = (await present(override, 500)) ? override : reject
      if (await present(ask, 1500)) {
        await d.click(ask.first(), { after: 1500 })
        const dialog = page.getByRole('dialog')
        if (await present(dialog)) await d.click(dialog.getByRole('button', { name: 'Cancel' }), { after: 900 })
      }
      await d.click(page.getByRole('tab', { name: /^Map/ }), { after: 1500 })
      await d.move(stage(page, 'prod-eu'))
      await d.pause(1200)
    },
  },
  {
    name: 'rollback-hold',
    title: 'Roll back prod and hold it there, after a confirmation',
    docs: ['rollback.md'],
    themes: ['light'],
    start: '#/p/shop/storefront',
    async run(d, page) {
      await d.pause(1200)
      await d.move(stage(page, 'prod'))
      await d.pause(600)
      await d.click(page.getByRole('button', { name: 'Roll back prod', exact: true }).first(), { after: 1200 })
      const dialog = page.getByRole('dialog')
      await d.move(dialog.getByRole('heading').first())
      await d.pause(900)
      // Hold prod on the rollback, with a reason, so nothing newer promotes over it.
      await d.click(dialog.getByRole('checkbox', { name: /^Hold prod/ }), { after: 800 })
      await d.click(dialog.getByRole('textbox', { name: /Reason/ }), { after: 300 })
      await d.type('INC-4211: 2.3.0 leaks connections')
      await d.pause(500)
      d.poster()
      await d.click(dialog.getByRole('button', { name: 'Roll back and hold prod', exact: true }), { after: 2400 })
    },
  },
  {
    name: 'compare',
    title: 'Compare two versions field by field',
    docs: ['installation.md'],
    themes: ['light'],
    start: '#/p/shop/checkout',
    async run(d, page) {
      await d.pause(1200)
      await d.click(page.locator('.bundle-chip', { hasText: '1-7-2' }), { modifiers: ['Shift'], after: 900 })
      await d.click(page.getByRole('button', { name: /^Compare/ }), { after: 1600 })
      d.poster()
      const panel = page.getByRole('dialog')
      await d.move(panel.getByText('Images').first())
      await d.pause(1200)
      await d.move(panel.getByText('Environments').first())
      await d.pause(1500)
      await d.press('Escape')
      await d.pause(600)
    },
  },
  {
    name: 'fleet',
    title: 'A big fleet: every pipeline on one board, a fleet of 42 targets',
    docs: ['pipeline-reference.md'],
    themes: ['light', 'dark'],
    fixtures: { big: true },
    start: '#/',
    async run(d, page) {
      await d.pause(1300)
      await d.move({ x: 760, y: 420 })
      await d.scroll(900)
      await d.scroll(-900)
      await d.click(page.getByRole('button', { name: 'edge-cdn', exact: true }), { after: 1800 })
      d.poster()
      // The station map (U2) zooms and has a minimap; use them when they are there.
      const zoomIn = page.getByRole('button', { name: /^Zoom in/ })
      if (await present(zoomIn, 800)) {
        await d.click(zoomIn, { after: 900 })
        await d.click(zoomIn, { after: 900 })
        const minimap = page.getByRole('img', { name: /minimap/i }).or(page.locator('[data-testid="minimap"]'))
        if (await present(minimap, 800)) await d.click(minimap.first(), { offset: { x: 40, y: 20 }, after: 1200 })
        const fit = page.getByRole('button', { name: /^(Fit|Zoom to fit)/ })
        if (await present(fit, 800)) await d.click(fit, { after: 1200 })
      } else {
        await d.move(page.locator('svg.dag-view__svg, [aria-label="Pipeline stages"]').first())
        await d.scroll(700)
        await d.scroll(-700)
      }
    },
  },
  {
    name: 'palette',
    title: 'Go anywhere with ⌘K',
    docs: ['installation.md'],
    themes: ['light'],
    start: '#/p/shop/checkout',
    async run(d, page) {
      await d.pause(1200)
      await d.press('Control+K', ['Ctrl', 'K'])
      await d.type('storefront prod')
      await d.pause(800)
      d.poster()
      await d.press('ArrowDown')
      await d.press('ArrowUp')
      await d.press('Enter', ['Enter'])
      await d.pause(1800)
    },
  },
]
