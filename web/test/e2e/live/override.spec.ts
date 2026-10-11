// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// UI-OVERRIDE-01: overriding a gate from the stage drawer (v0.10.1; #1245 had
// removed the old Override gate dialog). Go test: TestUI_BrowserGateOverride
// (test/e2e/live/ui_browser_test.go).
//
// In KARDINAL_UI_NAMESPACE, podinfo's Bundle is Verified in test and held
// before prod by gate hold-prod (false), whose instance is
// KARDINAL_UI_HOLD_INSTANCE. The drawer names it under "why it's waiting",
// with Override gate next to it. The dialog asks for a reason and a duration;
// Cancel writes nothing; confirming overrides the gate in prod
// (POST /api/v1/ui/override), and the Go test checks the instance.

import { test, expect } from '@playwright/test'
import { need, openPipeline, gateMark, stageDrawer, drawerHeading } from './live'

const base = need('KARDINAL_UI_URL')
const ns = need('KARDINAL_UI_NAMESPACE')
const holdGate = need('KARDINAL_UI_HOLD_GATE')
const holdInstance = need('KARDINAL_UI_HOLD_INSTANCE')

test('Override gate in the stage drawer asks first, then overrides the gate in prod', async ({ page }) => {
  const writes: string[] = []
  page.on('request', r => {
    // Reads, and the drawer's CEL check (POST validate-cel, which writes nothing).
    const path = new URL(r.url()).pathname
    // POST access (U7) asks what the caller may do: a read too.
    const reads = ['/api/v1/ui/validate-cel', '/api/v1/ui/access']
    if (r.method() !== 'GET' && !(r.method() === 'POST' && reads.includes(path))) writes.push(`${r.method()} ${path}`)
  })
  await openPipeline(page, base, ns)
  await gateMark(page, holdGate).click()
  const drawer = stageDrawer(page)
  await expect(drawerHeading(page)).toHaveText('prod')
  const why = drawer.locator('[data-section="why"] .stage-why__item').filter({ hasText: `Held by gate ${holdGate}.` })
  await expect(why).toBeVisible()
  // The gate's object, for kubectl, is under Kubernetes details (U4).
  const k8s = why.locator('details.k-k8s')
  await expect(k8s).toContainText('KindPolicyGate')
  await expect(k8s).toContainText(holdInstance)
  await expect(k8s).toContainText(ns)

  // Cancel writes nothing.
  await why.getByRole('button', { name: 'Override gate' }).click()
  const dialog = page.getByRole('dialog', { name: `Override ${holdGate} in prod?` })
  const confirm = dialog.getByRole('button', { name: 'Override gate' })
  await expect(confirm).toBeDisabled()
  await dialog.getByRole('button', { name: 'Cancel' }).click()
  await expect(dialog).toHaveCount(0)
  expect(writes).toEqual([])

  await why.getByRole('button', { name: 'Override gate' }).click()
  await dialog.getByLabel('Reason').fill('UI-OVERRIDE-01 hotfix')
  await dialog.getByLabel('For').selectOption('15')
  await confirm.click()
  await expect(dialog).toHaveCount(0)
  await expect(drawer.getByRole('status').filter({ hasText: 'Overridden' }))
    .toHaveText(new RegExp(`^Overridden\\. gate ${holdInstance} overridden in prod for 1 in-progress Bundle\\(s\\) until `))
  expect(writes).toEqual(['POST /api/v1/ui/override'])
})
