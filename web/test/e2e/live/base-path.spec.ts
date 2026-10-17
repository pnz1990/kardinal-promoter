// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UIAPI-BASEPATH-01: the UI under ui.basePath (#1933). Go test:
// TestUI_BrowserBasePath (test/e2e/live/ui_browser_test.go).
//
// kui-token (KARDINAL_UI_TOKEN_URL) serves the UI under KARDINAL_UI_BASE_PATH
// as well as at /ui/, with a static UI token (in the file
// KARDINAL_UI_TOKEN_FILE). A proxy that routes only the prefix would pass
// nothing else, so every request the page makes must stay under it.

import { readFileSync } from 'node:fs'
import { test, expect } from '@playwright/test'
import { need } from './live'

const origin = need('KARDINAL_UI_TOKEN_URL')
const base = need('KARDINAL_UI_BASE_PATH')
const token = readFileSync(need('KARDINAL_UI_TOKEN_FILE'), 'utf8').trim()

test('under the base path the app loads, signs in and calls its API there', async ({ page }) => {
  const paths: string[] = []
  const failed: string[] = []
  page.on('request', r => { if (r.url().startsWith(origin)) paths.push(new URL(r.url()).pathname) })
  page.on('response', r => {
    if (r.url().startsWith(origin) && r.status() >= 400 && !(r.status() === 401 && r.url().includes('/api/v1/ui/'))) failed.push(`${r.status()} ${r.url()}`)
  })

  const refused = page.waitForResponse(r => r.url() === `${origin}${base}/api/v1/ui/pipelines`)
  await page.goto(`${origin}${base}`)
  await expect(page).toHaveURL(`${origin}${base}/ui/`)
  expect((await refused).status()).toBe(401)

  const dialog = page.getByRole('dialog', { name: 'Sign in to kardinal' })
  await expect(dialog).toBeVisible()
  await dialog.getByLabel('Token').fill(token)
  const listed = page.waitForResponse(r => r.url() === `${origin}${base}/api/v1/ui/pipelines` && r.status() === 200)
  await dialog.getByRole('button', { name: 'Sign in' }).click()
  await listed
  await expect(dialog).toHaveCount(0)
  await expect(page.getByAltText('Kardinal')).toBeVisible()

  expect(failed).toEqual([])
  expect(paths.filter(p => !p.startsWith(`${base}/`))).toEqual([])
  expect(paths.some(p => p.startsWith(`${base}/ui/assets/`))).toBe(true)
})
