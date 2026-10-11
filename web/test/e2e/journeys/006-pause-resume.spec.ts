// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Journey 006: Pause → UI updates → Resume.

import { test, expect, type Page } from '@playwright/test'

// Serve the pipeline list with kardinal-test-app paused once `paused()` is true,
// so the test sees what the UI does after the controller has set spec.paused.
async function servePausedAfter(page: Page, paused: () => boolean) {
  await page.route('**/api/v1/ui/pipelines', async route => {
    const res = await route.fetch()
    const list = await res.json()
    for (const p of list) if (p.name === 'kardinal-test-app') p.paused = paused()
    await route.fulfill({ response: res, json: list })
  })
}

test.describe('Journey 006 — Pause and Resume pipeline', () => {
  test('Step 1: ActionBar shows Pause for a running pipeline', async ({ page }) => {
    await page.goto('/')
    await page.getByText('kardinal-test-app').first().click()
    const toolbar = page.getByRole('toolbar', { name: 'Pipeline actions' })
    await expect(toolbar.getByRole('button', { name: 'Pause pipeline' })).toBeVisible()
    await expect(toolbar.getByRole('button', { name: 'Resume pipeline' })).toHaveCount(0)
  })

  test('Step 2: Pause asks first, sends the pipeline and namespace, then shows PAUSED', async ({ page }) => {
    let paused = false
    await servePausedAfter(page, () => paused)
    await page.goto('/')
    await page.getByText('kardinal-test-app').first().click()

    await page.getByRole('toolbar', { name: 'Pipeline actions' }).getByRole('button', { name: 'Pause pipeline' }).click()
    const dialog = page.getByRole('dialog', { name: 'Pause pipeline?' })
    await expect(dialog).toBeVisible()

    const pauseRequest = page.waitForRequest(req => req.url().endsWith('/api/v1/ui/pause') && req.method() === 'POST')
    paused = true
    await dialog.getByRole('button', { name: 'Pause pipeline' }).click()
    const req = await pauseRequest
    expect(req.postDataJSON()).toEqual({ pipeline: 'kardinal-test-app', namespace: 'default' })

    await expect(dialog).toBeHidden()
    await expect(page.getByText(/PAUSED — no new promotions/)).toBeVisible()
    await expect(page.getByRole('button', { name: 'Resume pipeline' })).toBeVisible()
  })

  test('Step 3: A paused pipeline shows Resume, and Resume sends the pipeline and namespace', async ({ page }) => {
    let paused = true
    await servePausedAfter(page, () => paused)
    await page.goto('/')
    await page.getByText('kardinal-test-app').first().click()

    const toolbar = page.getByRole('toolbar', { name: 'Pipeline actions' })
    await expect(toolbar.getByRole('button', { name: 'Pause pipeline' })).toHaveCount(0)
    await toolbar.getByRole('button', { name: 'Resume pipeline' }).click()
    const dialog = page.getByRole('dialog', { name: 'Resume pipeline?' })

    const resumeRequest = page.waitForRequest(req => req.url().endsWith('/api/v1/ui/resume') && req.method() === 'POST')
    paused = false
    await dialog.getByRole('button', { name: 'Resume pipeline' }).click()
    expect((await resumeRequest).postDataJSON()).toEqual({ pipeline: 'kardinal-test-app', namespace: 'default' })
    await expect(toolbar.getByRole('button', { name: 'Pause pipeline' })).toBeVisible()
  })
})
