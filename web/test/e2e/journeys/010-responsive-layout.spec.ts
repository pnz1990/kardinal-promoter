// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Journey 010: Responsive layout at 1280px width (#799).
//
// Verifies that the UI produces no horizontal document overflow at a 1280×800
// viewport — the common laptop display resolution.
//
// Tests run at exactly 1280px (set per-test via page.setViewportSize) in both
// the idle state (pipeline list only) and the active state (pipeline selected,
// NodeDetail panel open). The assertion is that document.documentElement.scrollWidth
// never exceeds the viewport width.

import { test, expect } from '@playwright/test'

const VIEWPORT_1280 = { width: 1280, height: 800 }

test.describe('Journey 010 — Responsive layout at 1280px', () => {
  test('pipeline list state: no horizontal overflow at 1280×800', async ({ page }) => {
    await page.setViewportSize(VIEWPORT_1280)
    await page.goto('/')
    // Wait for the pipeline list to render
    await page.waitForSelector('text=kardinal-test-app', { timeout: 8000 })

    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth)
    expect(scrollWidth).toBeLessThanOrEqual(VIEWPORT_1280.width)
  })

  test('pipeline-selected state: no horizontal overflow at 1280×800', async ({ page }) => {
    await page.setViewportSize(VIEWPORT_1280)
    await page.goto('/')
    await page.waitForSelector('text=kardinal-test-app', { timeout: 8000 })

    // Select a pipeline to show DAG + header + lane view
    await page.getByText('kardinal-test-app').first().click()
    await page.waitForTimeout(300)

    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth)
    expect(scrollWidth).toBeLessThanOrEqual(VIEWPORT_1280.width)
  })

  test('node-detail-open state: no horizontal overflow at 1280×800', async ({ page }) => {
    await page.setViewportSize(VIEWPORT_1280)
    await page.goto('/')
    await page.waitForSelector('text=kardinal-test-app', { timeout: 8000 })

    // Select a pipeline
    await page.getByText('kardinal-test-app').first().click()
    await page.waitForTimeout(300)

    // Click the first DAG node to open NodeDetail split panel (340px)
    const firstNode = page.locator('svg [role="button"]').first()
    if (await firstNode.count() > 0) {
      await firstNode.click()
      await page.waitForTimeout(200)
    }

    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth)
    expect(scrollWidth).toBeLessThanOrEqual(VIEWPORT_1280.width)
  })
})
