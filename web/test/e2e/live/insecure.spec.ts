// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-INSECURE-01: the insecure-connection banner and its port-forward hint.
// Go test: TestUI_BrowserInsecureBanner (test/e2e/live/ui_browser_test.go).
//
// docs/installation.md: "If you expose port 8082 directly (e.g. via NodePort)
// without TLS, the UI will display a security warning". Loopback (the
// documented kubectl port-forward) and https are exempt.

import { test, expect, type Page } from '@playwright/test'
import { need, openPipeline } from './live'

const nodePortURL = need('KARDINAL_UI_NODEPORT_URL') // main release, plain HTTP, node IP
const tokenURL = need('KARDINAL_UI_TOKEN_URL') // kui-token: UI auth on, plain HTTP, node IP
const tlsURL = need('KARDINAL_UI_TLS_URL') // kui-tls: UI auth on, https
const forwardPort = need('KARDINAL_UI_PORT') // kubectl port-forward to the main release
const allowedHost = need('KARDINAL_UI_ALLOWED_HOST') // in the main release's ui.allowedHosts
const ns = need('KARDINAL_UI_NAMESPACE')

const HINT = 'kubectl port-forward svc/kardinal-promoter -n kardinal-system 8082:8082'

function banner(page: Page) {
  return page.getByRole('alert').filter({ hasText: 'Insecure connection' })
}

async function expectBanner(page: Page) {
  const b = banner(page)
  await expect(b).toBeVisible()
  await expect(b).toContainText('Insecure connection — kardinal UI is accessed over plain HTTP.')
  await expect(b.locator('code')).toHaveText(HINT)
  await expect(b.getByRole('link', { name: 'http://localhost:8082/ui/' })).toHaveAttribute('href', 'http://localhost:8082/ui/')
}

test('a NodePort over plain HTTP warns on the first page, before any pipeline loads', async ({ page }) => {
  // No UI auth mode: the API refuses the NodePort client, so the page never
  // gets past the pipeline list. The warning must still show.
  const refused = page.waitForResponse(r => r.url().includes('/api/v1/ui/pipelines'))
  await page.goto(`${nodePortURL}/ui/`)
  expect((await refused).status()).toBe(403)
  await expect(page.getByAltText('Kardinal')).toBeVisible()
  await expectBanner(page)
  await page.getByRole('button', { name: 'Dismiss insecure connection warning' }).click()
  await expect(banner(page)).toHaveCount(0)
})

test('a NodePort with UI auth over plain HTTP warns while it asks for the token', async ({ page }) => {
  await page.goto(`${tokenURL}/ui/`)
  const dialog = page.getByRole('dialog', { name: 'Sign in to kardinal' })
  await expect(dialog).toBeVisible()
  // The token is typed on this screen, so the warning has to be readable here.
  await expect(dialog).toContainText('plain HTTP')
})

test('an allowed host name over plain HTTP warns in the pipeline view', async ({ page }) => {
  await openPipeline(page, `http://${allowedHost}:${forwardPort}`, ns)
  await expectBanner(page)
})

for (const host of ['127.0.0.1', 'localhost']) {
  test(`the documented port-forward on ${host} shows no warning`, async ({ page }) => {
    await page.goto(`http://${host}:${forwardPort}/ui/`)
    await expect(page.getByRole('heading', { level: 2, name: 'Fleet' })).toBeVisible()
    await openPipeline(page, `http://${host}:${forwardPort}`, ns)
    await expect(banner(page)).toHaveCount(0)
  })
}

test.describe('https', () => {
  // kui-tls has a self-signed certificate for the node IP.
  test.use({ ignoreHTTPSErrors: true })

  test('the UI over TLS shows no warning', async ({ page }) => {
    await page.goto(`${tlsURL}/ui/`)
    const dialog = page.getByRole('dialog', { name: 'Sign in to kardinal' })
    await expect(dialog).toBeVisible()
    expect(new URL(page.url()).protocol).toBe('https:')
    await expect(banner(page)).toHaveCount(0)
    await expect(dialog).not.toContainText('plain HTTP')
  })
})
