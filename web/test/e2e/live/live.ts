// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// live.ts — helpers for the live browser specs (see playwright.live.config.ts).

import { expect, type Page } from '@playwright/test'

/**
 * need returns the environment variable the spec's Go test sets. It throws
 * when the variable is missing, so a spec run without its Go test fails
 * instead of skipping.
 */
export function need(name: string): string {
  const v = process.env[name]
  if (!v) throw new Error(`${name} is not set: run this spec from its Go test in test/e2e/live/ui_browser_test.go`)
  return v
}

/** needJSON is need, decoded from JSON. */
export function needJSON<T>(name: string): T {
  return JSON.parse(need(name)) as T
}

/** The pipeline every live test creates in its own namespace. */
export const PIPELINE = 'podinfo'

/** pipelineHash is the deep link to a pipeline (and optionally a node). */
export function pipelineHash(ns: string, pipeline = PIPELINE, extra: Record<string, string> = {}): string {
  return '#' + new URLSearchParams({ pipeline, ns, ...extra }).toString()
}

/**
 * openPipeline loads base/ui/ with the deep link to ns/pipeline and waits for
 * the pipeline header and its Bundle.
 */
export async function openPipeline(page: Page, base: string, ns: string, pipeline = PIPELINE, extra: Record<string, string> = {}) {
  await page.goto(`${base}/ui/${pipelineHash(ns, pipeline, extra)}`)
  await expect(page.getByRole('heading', { level: 1, name: pipeline })).toBeVisible()
}

/**
 * filterSidebar types query into the sidebar filter and waits for the list to
 * drop the other namespaces. The sidebar lists every test's pipelines (each
 * test's is called podinfo, in its own namespace); the filter matches the
 * name, the namespace or namespace/name.
 */
export async function filterSidebar(page: Page, query: string) {
  await page.getByRole('textbox', { name: 'Filter pipelines by name or namespace' }).fill(query)
  // The list applies the filter 150 ms after the last key. Until then
  // sidebarRow(page) matches the podinfo row of every namespace, and a click
  // on it fails in strict mode. A query here starts with a namespace (ns, or
  // ns/ and the start of a name), so wait until no namespace group the query
  // leaves out is listed.
  const ns = query.split('/')[0].toLowerCase()
  await expect.poll(() => page.getByRole('list', { name: 'Pipelines' }).evaluate((list, ns) =>
    Array.from(list.children)
      .filter(li => li.querySelector(':scope > ul[role="group"]'))
      .map(li => li.firstElementChild?.textContent?.trim() ?? '')
      .filter(header => !header.toLowerCase().includes(ns)), ns)).toEqual([])
}

/** sidebarRows are the sidebar's pipeline buttons (one per row). */
export function sidebarRows(page: Page) {
  return page.getByRole('list', { name: 'Pipelines' }).locator('button[aria-pressed]')
}

/**
 * sidebarRow is the sidebar button of the pipeline called name (after
 * filterSidebar has narrowed the list to one namespace). The cluster has many
 * namespaces, so the list is grouped; a row is the button whose name text is
 * exactly name.
 */
export function sidebarRow(page: Page, name = PIPELINE) {
  return sidebarRows(page).filter({ has: page.getByText(name, { exact: true }) })
}

/** dagNode is the DAG node of an environment or gate (its aria-label starts with the name). */
export function dagNode(page: Page, name: string) {
  return page.locator(`g.dag-node[aria-label^="${name} — "]`)
}

/**
 * collectErrors records the page's console errors and uncaught exceptions,
 * so a spec can require that there were none.
 */
export function collectErrors(page: Page): string[] {
  const errors: string[] = []
  page.on('console', msg => { if (msg.type() === 'error') errors.push(`console: ${msg.text()}`) })
  page.on('pageerror', err => errors.push(`pageerror: ${err.message}`))
  return errors
}
