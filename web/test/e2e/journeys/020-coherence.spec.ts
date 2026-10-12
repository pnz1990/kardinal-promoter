// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Journey 020 — one way to say things across the shell and the map: a version is its tag
// everywhere (the Bundle name only as quiet secondary text), the top bar keeps the full
// width when the stage drawer opens, and the comparison marks changes without the hold tone.
import { test, expect } from '@playwright/test'

test.describe('Journey 020 — Coherence', () => {
  test('a version reads as its tag on the rail, in the header, the Versions tab and the sidebar', async ({ page }) => {
    await page.goto('/ui/#/p/default/kardinal-test-app')
    const rail = page.getByRole('toolbar', { name: /^Versions/ })
    await expect(rail.locator('.bundle-chip__name')).toHaveText(['sha-9f8e7d6', 'config 77aa001', 'sha-abc1234']) // oldest to newest (Journey 021)
    const header = page.locator('.app-pipeline-head__summary')
    await expect(header.locator('.version-label__tag')).toHaveText('sha-abc1234')
    await expect(header.locator('.version-label')).toHaveAttribute('title', 'kardinal-test-app-abc123')
    await expect(page.locator('#app-sidebar, .app-sidebar').getByText('sha-abc1234', { exact: true })).toBeVisible()
    await page.getByRole('tab', { name: /^Versions/ }).click()
    await expect(page.getByRole('list', { name: 'Versions' }).locator('.version-label__tag').first()).toHaveText('sha-abc1234')
  })

  test('the stage drawer opens below a full-width top bar', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/ui/#/p/default/kardinal-test-app/env/prod')
    const drawer = page.getByTestId('stage-drawer')
    await expect(drawer).toBeVisible()
    const bar = (await page.locator('.app-topbar').boundingBox())!
    const box = (await drawer.boundingBox())!
    expect(box.y).toBeGreaterThanOrEqual(bar.y + bar.height - 1)
    expect(bar.x + bar.width).toBeGreaterThanOrEqual(1440 - 1)
    // One line: the breadcrumb does not wrap.
    const crumbs = (await page.getByRole('navigation', { name: 'Breadcrumb' }).boundingBox())!
    expect(crumbs.height).toBeLessThan(bar.height)
  })

  test('the comparison marks changed rows without the hold tone, and links fit their column', async ({ page }) => {
    // A phone-width comparison, so a CI run link has to give way (with 1fr columns the panel scrolls).
    await page.setViewportSize({ width: 400, height: 900 })
    await page.goto('/ui/#/p/default/kardinal-test-app?compare=kardinal-test-app-prev111')
    const dialog = page.getByRole('dialog', { name: /comparison/i })
    const changed = dialog.locator('.bundle-diff__row--field[data-changed]').first()
    await expect(changed).toBeVisible()
    // Like with like: the hold wash resolved on a probe element, against the row's computed background.
    const { row, hold } = await changed.evaluate((el) => {
      const probe = document.createElement('div')
      probe.style.background = 'var(--k-status-blocked-wash)'
      document.body.appendChild(probe)
      const hold = getComputedStyle(probe).backgroundColor
      probe.remove()
      return { row: getComputedStyle(el).backgroundColor, hold }
    })
    expect(hold).not.toBe('rgba(0, 0, 0, 0)')
    expect(row).not.toBe(hold)
    // Nothing in the dialog scrolls sideways, and the CI run link ends inside the dialog.
    const panel = dialog.locator('.bundle-diff')
    expect(await panel.evaluate((p) => p.scrollWidth - p.clientWidth)).toBeLessThanOrEqual(0)
    const link = dialog.locator('.bundle-diff__value--link').first()
    const [linkRight, panelRight] = await Promise.all([link.evaluate((a) => a.getBoundingClientRect().right), panel.evaluate((p) => p.getBoundingClientRect().right)])
    expect(linkRight).toBeLessThanOrEqual(panelRight)
  })

  test('⌘K finds a version by its tag, and the operations table shows the tag', async ({ page }) => {
    await page.goto('/ui/#/p/default/kardinal-test-app')
    await expect(page.getByRole('toolbar', { name: /^Versions/ })).toBeVisible()
    await page.getByRole('button', { name: /^Go to or do/ }).click()
    await page.getByRole('combobox').fill('sha-abc1234')
    await expect(page.getByRole('option', { name: /^Version sha-abc1234/ })).toBeVisible()
    await page.keyboard.press('Escape')
    await page.goto('/ui/#/?view=ops')
    await expect(page.getByRole('row', { name: /kardinal-test-app/ }).locator('.ops-table__bundle')).toHaveText('sha-abc1234')
  })

  test('one state per pipeline: the header, the sidebar badge and the counters agree', async ({ page }) => {
    await page.goto('/ui/#/p/default/kardinal-test-app')
    const header = page.getByTestId('pipeline-state')
    await expect(header).toHaveText(/Held by gate no-weekend-deploys in prod/)
    const sidebar = page.locator('.app-sidebar [data-category]').first()
    await expect(sidebar).toHaveAttribute('data-category', 'blocked')
    await expect(sidebar).toHaveAttribute('title', (await header.textContent())!.replace(/^\W+/, '').trim())
    await expect(page.getByRole('button', { name: /^0 pipelines currently promoting/ })).toBeVisible()
    // Following an older version names it by its tag in the breadcrumb.
    await page.getByRole('toolbar', { name: /^Versions/ }).locator('.bundle-chip').first().click()
    await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('sha-9f8e7d6')
  })
})
