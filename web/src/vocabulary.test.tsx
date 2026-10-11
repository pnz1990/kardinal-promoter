// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// vocabulary.test.tsx — the UI speaks the glossary (UX-AUDIT.md §4.3, U4): a stage, a gate, a
// version. The Kubernetes kinds (PromotionStep, PolicyGate) appear only inside a "Kubernetes
// details" disclosure, and "Reconciling" never appears.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, act, fireEvent } from '@testing-library/react'
import type { GraphResponse, PolicyGate } from './types'

vi.mock('@dagrejs/dagre', () => {
  function MockGraph(this: Record<string, unknown>) {
    this.setGraph = vi.fn(); this.setDefaultEdgeLabel = vi.fn(); this.setNode = vi.fn(); this.setEdge = vi.fn()
    this.node = vi.fn().mockReturnValue({ x: 100, y: 80 })
  }
  return { default: { graphlib: { Graph: MockGraph }, layout: vi.fn() } }
})

const graph: GraphResponse = {
  nodes: [
    { id: 'app-v1-test', type: 'PromotionStep', label: 'test', environment: 'test', state: 'Verified' },
    { id: 'app-v1-prod', type: 'PromotionStep', label: 'prod', environment: 'prod', state: 'Promoting' },
    { id: 'soak-app-v1-prod', type: 'PolicyGate', label: 'soak', environment: 'prod', state: 'Block', holding: true, expression: 'upstream.test.soakMinutes >= 30' },
  ],
  edges: [{ from: 'app-v1-test', to: 'soak-app-v1-prod' }, { from: 'soak-app-v1-prod', to: 'app-v1-prod' }],
}
const gates: PolicyGate[] = [{
  name: 'soak-app-v1-prod', namespace: 'default', expression: 'upstream.test.soakMinutes >= 30', ready: false,
  reason: 'soak 0m < 30m', pipeline: 'app', bundle: 'app-v1', environment: 'prod', state: 'Block', holding: true,
} as PolicyGate]

vi.mock('./api/client', () => ({
  api: {
    listPipelines: vi.fn(async () => [
      { name: 'app', namespace: 'default', phase: 'Promoting', environmentCount: 2, activeBundleName: 'app-v1', blockerCount: 1, failedStepCount: 0 },
      { name: 'other', namespace: 'default', phase: 'Ready', environmentCount: 1, failedStepCount: 1 },
    ]),
    listGates: vi.fn(async () => gates),
    listBundles: vi.fn(async () => [{ name: 'app-v1', namespace: 'default', phase: 'Promoting', type: 'image', pipeline: 'app', createdAt: new Date().toISOString() }]),
    getGraph: vi.fn(async () => graph),
    getSteps: vi.fn(async () => []),
    getStepEvents: vi.fn(async () => []),
    validateCEL: vi.fn(async () => ({ valid: true })),
  },
}))

import { App } from './App'

/** The words a person reads: text and accessible names, outside <details> disclosures. */
function readable(root: HTMLElement): string {
  const clone = root.cloneNode(true) as HTMLElement
  clone.querySelectorAll('details').forEach((d) => d.remove())
  const names = [...clone.querySelectorAll('[title],[aria-label],[alt],[placeholder]')]
    .flatMap((e) => ['title', 'aria-label', 'alt', 'placeholder'].map((a) => e.getAttribute(a) ?? ''))
  return [clone.textContent ?? '', ...names].join('\n')
}

// The kinds, the controller's words, and raw state values that have a display label.
const RAW = /PromotionStep|PolicyGate|Reconciling|\b[Bb]undles?\b|WaitingForMerge|HealthChecking|PromotionStep|\bBlock\b|\bSup\b/

/** Each raw word with the text around it, for a readable failure. */
function context(text: string): string[] {
  return [...text.matchAll(new RegExp(RAW, 'g'))].map((m) => text.slice(Math.max(0, m.index! - 30), m.index! + 40).replace(/\s+/g, ' '))
}

async function flush(ms = 50) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  localStorage.clear()
})
afterEach(() => { vi.useRealTimers() })

describe('vocabulary', () => {
  it('the fleet, the operations table and every pipeline tab say stage, gate and version', async () => {
    window.history.replaceState(null, '', '/ui/#/')
    render(<App />)
    await flush()
    expect.soft(context(readable(document.body)), 'fleet').toEqual([])

    fireEvent.click(screen.getByRole('button', { name: 'Switch to Operations Table' }))
    await flush()
    expect.soft(context(readable(document.body)), 'operations table').toEqual([])

    window.history.replaceState(null, '', '/ui/#/p/default/app')
    await act(async () => { window.dispatchEvent(new PopStateEvent('popstate')) })
    await flush()
    for (const tab of ['Map', 'Versions', 'Activity', 'Metrics']) {
      fireEvent.click(screen.getByRole('tab', { name: new RegExp(`^${tab}`) }))
      await flush()
      expect.soft(context(readable(document.body)), `${tab} tab`).toEqual([])
    }
  })

  it('the details of a stage and of a gate keep the kinds under Kubernetes details', async () => {
    for (const [route, kind] of [['#/p/default/app/env/prod', 'PromotionStep'], ['#/p/default/app?node=soak-app-v1-prod', 'PolicyGate']]) {
      window.history.replaceState(null, '', `/ui/${route}`)
      const { unmount } = render(<App />)
      await flush()
      const detail = screen.getByTestId('node-detail')
      expect.soft(context(readable(detail)), route).toEqual([])
      const k8s = detail.querySelector('details')
      expect(k8s?.textContent).toContain(kind)
      unmount()
    }
  })
})
