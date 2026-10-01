// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-STATIC-01: /ui/ serves the embedded web app (docs/installation.md,
// "Accessing the UI"). Go test: TestUI_BrowserStatic
// (test/e2e/live/ui_browser_test.go), which checks the files over HTTP.
//
// KARDINAL_UI_NAMESPACE has Pipeline podinfo, with no Bundle.

import { test, expect } from '@playwright/test'
import { need, collectErrors, filterSidebar, sidebarRow, PIPELINE } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')

test('/ui/ starts the web app: every file it needs loads, and it shows the landing page and the pipelines', async ({ page }) => {
  const errors = collectErrors(page)
  const failed: string[] = []
  page.on('requestfailed', req => failed.push(`${req.method()} ${req.url()}: ${req.failure()?.errorText}`))
  page.on('response', resp => { if (resp.status() >= 400) failed.push(`${resp.request().method()} ${resp.url()}: HTTP ${resp.status()}`) })

  const index = await page.goto(`${base}/ui/`)
  expect(index?.status()).toBe(200)
  await expect(page).toHaveTitle('Kardinal Promoter')

  // The script ran and the stylesheet applied (it defines the theme colors).
  await expect(page.getByText('KARDINAL', { exact: true })).toBeVisible()
  await expect.poll(() => page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--color-bg').trim())).toMatch(/^#[0-9a-f]{6}$/)
  const logo = page.getByRole('img', { name: 'Kardinal', exact: true })
  await expect(logo).toBeVisible()
  await expect.poll(() => logo.evaluate((img: HTMLImageElement) => img.complete ? img.naturalWidth : 0)).toBeGreaterThan(0)

  // The landing page, with the cluster's pipelines in the sidebar.
  await expect(page.getByText('Select a pipeline to view its promotion DAG.')).toBeVisible()
  await filterSidebar(page, ns)
  await expect(sidebarRow(page)).toHaveCount(1)
  await sidebarRow(page).click()
  await expect(page.getByRole('heading', { level: 1, name: PIPELINE })).toBeVisible()

  expect(failed).toEqual([])
  expect(errors).toEqual([])
})
