// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-ACTIONS-01: promote, rollback, pause and resume from the UI. Go test:
// TestUI_BrowserActions (test/e2e/live/ui_browser_test.go).
//
// The Go test runs this spec once per step of the scenario, named by
// KARDINAL_UI_ACTION, and checks the cluster after each: pause, resume,
// promote, rollback. So each run defines the one test of its step.
//
// podinfo has test and prod. Its first Bundle is Verified in both; the
// newest stopped at test (intent.targetEnvironment), so prod can be
// promoted. After the promote step, prod runs the promoted Bundle and can be
// rolled back.

import { test, expect, type Page } from '@playwright/test'
import { need, openPipeline, dagNode, filterSidebar, sidebarRow, PIPELINE, stageDrawer, drawerHeading } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const action = need('KARDINAL_UI_ACTION')

const toolbar = (page: Page) => page.getByRole('toolbar', { name: 'Pipeline actions' })
const lane = (page: Page) => page.getByLabel('Pipeline stages')
const pausedHeader = (page: Page) => page.getByText('⏸ PAUSED — no new promotions')
/** The sidebar's PAUSED badge on this test's pipeline. */
async function sidebarBadge(page: Page) {
  await filterSidebar(page, ns)
  return sidebarRow(page).getByTitle('Pipeline is paused — no new promotions will start')
}

const steps: Record<string, () => void> = {
  pause: () => test('Pause in the action bar asks first, then pauses the pipeline', async ({ page }) => {
    await openPipeline(page, base, ns)
    await expect(pausedHeader(page)).toHaveCount(0)

    // Cancel changes nothing.
    await toolbar(page).getByRole('button', { name: 'Pause pipeline' }).click()
    let dialog = page.getByRole('dialog', { name: 'Pause pipeline?' })
    await expect(dialog).toHaveAccessibleDescription(/No new promotion step starts for "podinfo"/)
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toHaveCount(0)
    await expect(toolbar(page).getByRole('button', { name: 'Pause pipeline' })).toBeVisible()

    await toolbar(page).getByRole('button', { name: 'Pause pipeline' }).click()
    dialog = page.getByRole('dialog', { name: 'Pause pipeline?' })
    await dialog.getByRole('button', { name: 'Pause pipeline' }).click()
    await expect(dialog).toHaveCount(0)
    await expect(pausedHeader(page)).toBeVisible()
    await expect(toolbar(page).getByRole('button', { name: 'Resume pipeline' })).toBeVisible()
    await expect(toolbar(page).getByRole('alert')).toHaveCount(0)
    await expect(await sidebarBadge(page)).toHaveText('PAUSED')
  }),

  resume: () => test('Resume in the action bar asks first, then resumes the pipeline', async ({ page }) => {
    await openPipeline(page, base, ns)
    await expect(pausedHeader(page)).toBeVisible()
    await expect(await sidebarBadge(page)).toHaveText('PAUSED')
    await toolbar(page).getByRole('button', { name: 'Resume pipeline' }).click()
    const dialog = page.getByRole('dialog', { name: 'Resume pipeline?' })
    await expect(dialog).toHaveAccessibleDescription('Held promotion steps for "podinfo" continue where they stopped.')
    await dialog.getByRole('button', { name: 'Resume pipeline' }).click()
    await expect(dialog).toHaveCount(0)
    await expect(pausedHeader(page)).toHaveCount(0)
    await expect(toolbar(page).getByRole('button', { name: 'Pause pipeline' })).toBeVisible()
    await expect(await sidebarBadge(page)).toHaveCount(0)
  }),

  promote: () => test('Promote in the stage lane asks first, then starts a promotion to prod', async ({ page }) => {
    await openPipeline(page, base, ns)
    await expect(dagNode(page, 'test')).toHaveAttribute('aria-label', 'test — Live')
    await expect(dagNode(page, 'prod')).toHaveAttribute('aria-label', 'prod — Not reached')
    // test is the first environment: CI starts it, so it has no Promote.
    await expect(lane(page).getByRole('button', { name: 'Promote to test' })).toHaveCount(0)

    // The stage drawer offers the same action, and none for test.
    const detail = stageDrawer(page)
    await dagNode(page, 'test').click()
    await expect(drawerHeading(page)).toHaveText('test')
    await expect(detail.locator('[data-action="promote"]')).toHaveCount(0)
    await dagNode(page, 'prod').click()
    await expect(drawerHeading(page)).toHaveText('prod')
    await expect(detail.locator('[data-action="promote"]')).toHaveText('Promote')
    await detail.getByRole('button', { name: 'Close prod' }).click()
    await expect(detail).toHaveCount(0)

    // Cancel creates nothing (the Go test checks).
    await lane(page).getByRole('button', { name: 'Promote to prod' }).click()
    await page.getByRole('dialog', { name: `Promote ${PIPELINE} to prod?` }).getByRole('button', { name: 'Cancel' }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)

    await lane(page).getByRole('button', { name: 'Promote to prod' }).click()
    const dialog = page.getByRole('dialog', { name: `Promote ${PIPELINE} to prod?` })
    await expect(dialog).toHaveAccessibleDescription(`Creates a new bundle for ${PIPELINE} that targets prod. Its progress shows in the bundle timeline.`)
    await dialog.getByRole('button', { name: 'Promote to prod' }).click()
    await expect(dialog).toHaveCount(0)
    await expect(page.getByRole('status').filter({ hasText: 'Promotion started' })).toHaveText(/^Promotion started: bundle podinfo-[a-z0-9-]+$/)
  }),

  rollback: () => test('Roll back in the stage drawer asks first, then rolls prod back', async ({ page }) => {
    await openPipeline(page, base, ns)
    await expect(dagNode(page, 'prod')).toHaveAttribute('aria-label', 'prod — Live')
    // The stage lane offers the same action.
    await expect(lane(page).getByRole('button', { name: 'Roll back prod' })).toBeVisible()

    await dagNode(page, 'prod').click()
    const detail = stageDrawer(page)
    await detail.locator('[data-action="rollback"]').click()
    // The drawer offers the versions Verified in prod; the one before is first.
    const dialog = page.getByRole('dialog', { name: /^Roll prod back( to .+)?\?$/ })
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toHaveCount(0)
    await detail.locator('[data-action="rollback"]').click()
    await dialog.getByRole('button', { name: 'Roll back' }).click()
    await expect(dialog).toHaveCount(0)
    await expect(detail.getByRole('status').filter({ hasText: 'Rolled back' }))
      .toHaveText(/^Rolled back\. rollback started — rolling prod back to [a-z0-9-]+$/)
  }),
}

const define = steps[action]
if (!define) throw new Error(`KARDINAL_UI_ACTION=${action}: want one of ${Object.keys(steps).join(', ')}`)
define()
