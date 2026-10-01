// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-CREATE-01: the Create Bundle dialog. Go test: TestUI_BrowserCreateBundle
// (test/e2e/live/ui_browser_test.go).
//
// podinfo in KARDINAL_UI_NAMESPACE has one environment and no Bundle. The
// spec creates one for KARDINAL_UI_IMAGE with a commit SHA and an author;
// the Go test then checks it is the only Bundle and that it promotes.

import { test, expect, type Page } from '@playwright/test'
import { need, openPipeline, PIPELINE } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const image = need('KARDINAL_UI_IMAGE')
const commitSHA = need('KARDINAL_UI_COMMIT_SHA')
const author = need('KARDINAL_UI_AUTHOR')

const openDialog = async (page: Page) => {
  await page.getByRole('button', { name: `Create bundle for ${PIPELINE}` }).click()
  const dialog = page.getByRole('dialog', { name: `Create Bundle — ${PIPELINE}` })
  await expect(dialog).toBeVisible()
  return dialog
}

test('Create Bundle checks the image, reports a refusal, and creates a Bundle that shows on screen', async ({ page }) => {
  await openPipeline(page, base, ns)
  const chips = page.locator('.bundle-chip')
  await expect(chips).toHaveCount(0)

  let dialog = await openDialog(page)
  const imageInput = dialog.getByLabel('Container image')
  await expect(imageInput).toBeFocused()

  // No image: the dialog says so and sends nothing.
  await dialog.getByRole('button', { name: 'Create bundle' }).click()
  await expect(dialog.getByRole('alert')).toHaveText('Image reference is required.')
  await expect(imageInput).toHaveAccessibleDescription('Image reference is required.')

  // An image with no repository: the API refuses it, and the dialog shows
  // why and stays open.
  await imageInput.fill(':' + image.split(':').pop())
  await expect(dialog.getByRole('alert')).toHaveCount(0)
  await dialog.getByRole('button', { name: 'Create bundle' }).click()
  await expect(dialog.getByRole('alert')).toContainText('Could not create the bundle: API error 400: bundle rejected by validation: ')
  await expect(dialog).toBeVisible()

  // Cancel closes it; the next one starts empty.
  await dialog.getByRole('button', { name: 'Cancel bundle creation' }).click()
  await expect(dialog).toHaveCount(0)
  dialog = await openDialog(page)
  await expect(dialog.getByLabel('Container image')).toHaveValue('')

  await dialog.getByLabel('Container image').fill(image)
  await dialog.getByLabel('Commit SHA').fill(commitSHA)
  await dialog.getByLabel('Author').fill(author)
  await dialog.getByRole('button', { name: 'Create bundle' }).click()
  await expect(dialog).toHaveCount(0)

  // The new Bundle is on screen, with its provenance.
  await expect(chips).toHaveCount(1)
  await expect(page.getByRole('button', { name: /^Copy bundle name "podinfo-[a-z0-9]+"$/ })).toBeVisible()
  await expect(page.getByTitle('Commit SHA', { exact: true })).toHaveText(commitSHA.slice(0, 8))
  await expect(page.getByTitle('Author', { exact: true })).toHaveText(author)
})
