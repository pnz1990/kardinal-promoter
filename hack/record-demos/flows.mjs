// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// flows.mjs — the scripted clips. Each flow starts at a route of the UI, runs against
// the fixtures (fixtures.mjs), marks its poster frame, and fails when the step its name
// promises is not on screen (expect()), so a UI change that breaks a clip fails
// `record.mjs --check` instead of recording the wrong thing.
import { expect } from 'playwright/test'

/** A station on the map, by environment: its accessible name starts "<env> — ". */
const station = (page, env) => page.getByRole('button', { name: new RegExp(`^${env} — `) }).first()
/** The stage drawer (a column beside the map, or a sheet on narrow screens). */
const drawer = (page) => page.getByTestId('stage-drawer')
/** The version rail's chip for a version (the Bundle name without the pipeline prefix). */
const chip = (page, version) => page.getByRole('toolbar', { name: /^Versions/ }).getByRole('button', { name: new RegExp(`^${version} `) })

export const FLOWS = [
  {
    name: 'first-look',
    title: 'From the fleet to a pipeline map, following a version',
    themes: ['light', 'dark'],
    start: '#/',
    async run(d, page) {
      await d.pause(1400)
      await d.click(page.getByRole('button', { name: 'checkout', exact: true }), { after: 1600 })
      await expect(station(page, 'prod-eu')).toBeVisible()
      d.poster()
      await d.move(station(page, 'uat'))
      await d.pause(600)
      await d.move(station(page, 'prod-eu'))
      await d.pause(900)
      // Follow the version prod runs today on the rail, then come back to the new one.
      await d.click(chip(page, '1-7-2'), { after: 1700 })
      await d.click(chip(page, '1-8-0'), { after: 1400 })
    },
  },
  {
    name: 'why-waiting',
    title: 'Why a stage is waiting: the drawer opens on its reason',
    themes: ['light'],
    start: '#/p/shop/checkout',
    async run(d, page) {
      await d.pause(1200)
      await d.click(station(page, 'prod-us'), { after: 1400 })
      const why = drawer(page).getByRole('region', { name: 'Why it’s waiting' })
      await expect(why).toBeVisible()
      await d.move(why.getByRole('listitem').first())
      d.poster()
      await d.pause(1500)
      // The reason's rule, behind Details.
      await d.click(why.getByText('Details', { exact: true }).first(), { after: 1500 })
      await d.move(drawer(page).getByRole('heading', { name: 'Actions' }))
      await d.pause(1200)
    },
  },
  {
    name: 'approve-override',
    title: 'Approve a release, then override a gate after its confirmation',
    themes: ['light'],
    start: '#/p/shop/checkout/env/prod-eu',
    async run(d, page) {
      await d.pause(1300)
      const why = drawer(page).getByRole('region', { name: 'Why it’s waiting' })
      await d.move(why.getByText(/Needs 1 more approval/).first())
      await d.pause(700)
      await d.click(why.getByRole('button', { name: 'Approve' }).first(), { after: 1200 })
      const approve = page.getByRole('dialog')
      if (await approve.isVisible().catch(() => false)) {
        await d.click(approve.getByRole('button', { name: /^Approve/ }).last(), { after: 1500 })
      }
      // prod-us is held by a CEL gate: Override asks for a reason and a duration first.
      await d.click(station(page, 'prod-us'), { after: 1200 })
      await d.click(drawer(page).getByRole('button', { name: 'Override gate' }).first(), { after: 900 })
      const dialog = page.getByRole('dialog', { name: /^Override / })
      await expect(dialog).toBeVisible()
      await d.click(dialog.getByRole('textbox').first(), { after: 300 })
      await d.type('INC-4302: soak timer reset by the uat redeploy')
      await d.pause(500)
      d.poster()
      await d.click(dialog.getByRole('button', { name: 'Override gate' }), { after: 2000 })
    },
  },
  {
    name: 'rollback-hold',
    title: 'Roll back prod and hold it there, after a confirmation',
    themes: ['light'],
    start: '#/p/shop/storefront',
    async run(d, page) {
      await d.pause(1200)
      await d.click(station(page, 'prod'), { after: 1200 })
      await d.click(drawer(page).getByRole('button', { name: /^Roll back/ }).first(), { after: 1100 })
      const dialog = page.getByRole('dialog')
      await expect(dialog).toBeVisible()
      await d.move(dialog.getByRole('heading').first())
      await d.pause(800)
      // Hold prod on the rollback, with a reason, so nothing newer promotes over it.
      await d.click(dialog.getByRole('checkbox', { name: /^Hold/ }), { after: 700 })
      await d.click(dialog.getByRole('textbox', { name: /Why hold|Reason/ }), { after: 300 })
      await d.type('INC-4211: 2.3.0 leaks connections')
      await d.pause(500)
      d.poster()
      await d.click(dialog.getByRole('button', { name: /^Roll back/ }).last(), { after: 2400 })
    },
  },
  {
    name: 'compare',
    title: 'Compare two versions from the version rail',
    themes: ['light'],
    start: '#/p/shop/checkout',
    async run(d, page) {
      await d.pause(1200)
      await d.move(chip(page, '1-8-0'))
      await d.pause(500)
      await d.click(chip(page, '1-7-2'), { modifiers: ['Shift'], after: 900 })
      await d.click(page.getByRole('button', { name: /^Compare/ }).first(), { after: 1600 })
      const panel = page.getByRole('dialog', { name: /comparison/i })
      await expect(panel).toBeVisible()
      d.poster()
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
    title: 'A big fleet: the board, then a fleet of 42 targets at three zoom levels with the minimap',
    themes: ['light', 'dark'],
    fixtures: { big: true },
    start: '#/',
    async run(d, page) {
      await d.pause(1200)
      await d.move({ x: 760, y: 420 })
      await d.scroll(900)
      await d.scroll(-900)
      await d.click(page.getByRole('button', { name: 'edge-cdn', exact: true }), { after: 1500 })
      const levels = page.getByRole('radiogroup', { name: 'Zoom level' })
      await expect(levels).toBeVisible()
      // Overview, with the minimap in the corner: a click there centres the view on that point.
      const minimap = page.locator('.station-map__minimap')
      await expect(minimap).toBeVisible()
      await d.click(minimap, { after: 1000 })
      // Map → Detail, then open the fleet's grid of targets and fit it on screen (0).
      await d.click(levels.getByRole('radio', { name: 'Map' }), { after: 1100 })
      await d.click(levels.getByRole('radio', { name: 'Detail' }), { after: 1100 })
      await d.click(page.getByRole('button', { name: /^pops · 42 targets/ }), { after: 1000 })
      await d.press('0', ['0'])
      await d.pause(900)
      d.poster()
      // Zoom in on the grid with the wheel, then back to the whole map.
      const grid = page.getByRole('button', { name: /^pops-/ }).nth(12)
      await d.move(grid)
      for (let i = 0; i < 3; i++) { await page.mouse.wheel(0, -200); await d.pause(380) }
      await d.pause(900)
      await d.press('0', ['0'])
      await d.pause(1200)
    },
  },
  {
    name: 'palette',
    title: 'Go anywhere with ⌘K',
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
      await expect(drawer(page)).toBeVisible()
      await d.pause(1800)
    },
  },
]
