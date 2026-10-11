// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// vocabulary.test.tsx — the UI speaks the glossary (UX-AUDIT.md §4.3, U4): a stage, a gate, a
// version. The Kubernetes kinds (PromotionStep, PolicyGate, Bundle) appear only inside the
// "Kubernetes details" disclosure (.k-k8s), "Reconciling" never appears, and no raw API state
// shows where it has a display label: the raw values are generated from displayStates.gen.ts,
// so a new state is checked as soon as pkg/display has it.
//
// The walk covers every screen and transient surface: the fleet, the operations table, every
// pipeline tab, both detail panels, each confirm dialog and the message after it, the approvals
// list, the ship and compare dialogs, help, the palette, an empty pipeline, an empty cluster and
// a failed read.
import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest'
import { render, screen, act, fireEvent, within, cleanup } from '@testing-library/react'
import type { Bundle, GraphResponse, Pipeline, PolicyGate } from './types'
import { DISPLAY_STATES } from './displayStates.gen'

vi.mock('@dagrejs/dagre', () => {
  function MockGraph(this: Record<string, unknown>) {
    this.setGraph = vi.fn(); this.setDefaultEdgeLabel = vi.fn(); this.setNode = vi.fn(); this.setEdge = vi.fn()
    this.node = vi.fn().mockReturnValue({ x: 100, y: 80 })
  }
  return { default: { graphlib: { Graph: MockGraph }, layout: vi.fn() } }
})

const now = Date.now()
const ago = (min: number) => new Date(now - min * 60_000).toISOString()

/** Every state of the fixture's graph: the step states, and approval, waiting, superseded and passing gates. */
const graph: GraphResponse = {
  nodes: [
    { id: 'app-v2-test', type: 'PromotionStep', label: 'test', environment: 'test', state: 'Verified' },
    { id: 'app-v2-uat', type: 'PromotionStep', label: 'uat', environment: 'uat', state: 'HealthChecking', startedAt: ago(2) },
    { id: 'app-v2-stage', type: 'PromotionStep', label: 'stage', environment: 'stage', state: 'WaitingForMerge', prURL: 'https://github.com/o/r/pull/7' },
    { id: 'app-v2-prod', type: 'PromotionStep', label: 'prod', environment: 'prod', state: 'NotStarted' },
    { id: 'app-v2-dr', type: 'PromotionStep', label: 'dr', environment: 'dr', state: 'AbortedByAlarm' },
    { id: 'soak-app-v2-prod', type: 'PolicyGate', label: 'soak', environment: 'prod', state: 'Block', holding: true, expression: 'upstream.uat.soakMinutes >= 30' },
    { id: 'approval-app-v2-prod', type: 'PolicyGate', label: 'release-approval', environment: 'prod', state: 'Waiting', expression: 'true' },
    { id: 'hours-app-v2-stage', type: 'PolicyGate', label: 'business-hours', environment: 'stage', state: 'Pass', expression: 'schedule.hour >= 9' },
  ],
  edges: [
    { from: 'app-v2-test', to: 'app-v2-uat' }, { from: 'app-v2-uat', to: 'hours-app-v2-stage' },
    { from: 'hours-app-v2-stage', to: 'app-v2-stage' }, { from: 'app-v2-stage', to: 'soak-app-v2-prod' },
    { from: 'soak-app-v2-prod', to: 'approval-app-v2-prod' }, { from: 'approval-app-v2-prod', to: 'app-v2-prod' },
    { from: 'app-v2-test', to: 'app-v2-dr' },
  ],
}
const gates = [
  { name: 'soak-app-v2-prod', namespace: 'default', expression: 'upstream.uat.soakMinutes >= 30', ready: false, reason: 'soak 0m < 30m', pipeline: 'app', bundle: 'app-v2', environment: 'prod', state: 'Block', holding: true },
  { name: 'approval-app-v2-prod', namespace: 'default', expression: 'true', ready: false, reason: 'waiting for approvals: 1 of 2', pipeline: 'app', bundle: 'app-v2', environment: 'prod', state: 'Waiting',
    approval: { required: 2, allowedGroups: ['release-managers'], excludeAuthor: true, approved: 1, decisions: [{ user: 'alice', decision: 'approve', counted: true, comment: 'ok' }, { user: 'ci-bot', decision: 'approve', counted: false, reason: "the version's author (approval.excludeAuthor)" }] } },
  { name: 'hours-app-v2-stage', namespace: 'default', expression: 'schedule.hour >= 9', ready: true, pipeline: 'app', bundle: 'app-v2', environment: 'stage', state: 'Pass' },
  { name: 'soak-app-v1-prod', namespace: 'default', expression: 'upstream.uat.soakMinutes >= 30', ready: false, pipeline: 'app', bundle: 'app-v1', environment: 'prod', state: 'Superseded' },
  { name: 'soak', namespace: 'default', expression: 'upstream.uat.soakMinutes >= 30', ready: false, template: true, state: 'Pending' },
] as PolicyGate[]
const bundles: Bundle[] = [
  { name: 'app-v2', namespace: 'default', phase: 'Promoting', type: 'image', pipeline: 'app', createdAt: ago(10),
    provenance: { commitSHA: 'abcdef1234567', author: 'dev', ciRunURL: 'https://ci.example/run/1' },
    environments: [{ name: 'test', phase: 'Verified' }, { name: 'uat', phase: 'HealthChecking' }] },
  { name: 'app-v1', namespace: 'default', phase: 'Superseded', type: 'config', pipeline: 'app', createdAt: ago(600),
    // Live in the final environments (prod, dr), so the Metrics tab has numbers.
    environments: ['test', 'uat', 'stage', 'prod', 'dr'].map((name) => ({ name, phase: 'Verified', healthCheckedAt: ago(500) })) },
  { name: 'app-v0', namespace: 'default', phase: 'Rejected', type: 'image', pipeline: 'app', createdAt: ago(1200), rejected: { by: 'bob', reason: 'CVE' } },
]
const topology = [
  { name: 'test' }, { name: 'uat', upstreams: ['test'] }, { name: 'stage', upstreams: ['uat'] },
  { name: 'prod', upstreams: ['stage'] }, { name: 'dr', upstreams: ['test'] },
]
const pipelines: Pipeline[] = [
  { name: 'app', namespace: 'default', phase: 'Promoting', environmentCount: 5, activeBundleName: 'app-v2', blockerCount: 1, failedStepCount: 1, inventoryAgeDays: 3,
    lastMergedAt: ago(30), topologyResolved: true, environmentTopology: topology,
    environmentStates: { test: 'Verified', uat: 'HealthChecking', stage: 'WaitingForMerge', prod: 'NotStarted', dr: 'AbortedByAlarm' },
    deploymentMetrics: { rolloutsLast30Days: 4, p50CommitToProdMinutes: 42, deployments: 4, failedDeployments: 1, changeFailureRateMillis: 250 } },
  { name: 'empty', namespace: 'default', phase: 'Unknown', environmentCount: 2, environmentTopology: [{ name: 'test' }, { name: 'prod', upstreams: ['test'] }], topologyResolved: true },
  { name: 'paused', namespace: 'default', phase: 'Ready', environmentCount: 1, paused: true, failedStepCount: 0, environmentStates: { prod: 'Superseded' } },
]

const steps = (['test', 'uat', 'stage', 'dr'] as const).map((env) => ({
  name: `app-v2-${env}`, namespace: 'default', pipeline: 'app', bundle: 'app-v2', environment: env, stepType: 'kustomize-set-image',
  state: graph.nodes.find((n) => n.environment === env && n.type === 'PromotionStep')!.state,
}))

const h = vi.hoisted(() => ({ failPipelines: false, noPipelines: false }))

vi.mock('./api/client', () => ({
  api: {
    listPipelines: vi.fn(async () => {
      if (h.failPipelines) throw new Error('API error 503: Service Unavailable')
      return h.noPipelines ? [] : pipelines
    }),
    listGates: vi.fn(async () => gates),
    listBundles: vi.fn(async (name: string) => (name === 'app' ? bundles : [])),
    getGraph: vi.fn(async (name: string) => (name.startsWith('app-') ? graph : { nodes: [], edges: [] })),
    getSteps: vi.fn(async (name: string) => (name === 'app-v2' ? steps : [])),
    getStepEvents: vi.fn(async () => []),
    validateCEL: vi.fn(async () => ({ valid: true })),
    rollback: vi.fn(async () => ({ bundle: 'app-rollback', message: 'ok' })),
    promote: vi.fn(async () => ({ bundle: 'app-promote', message: 'ok' })),
    recordApproval: vi.fn(async () => ({ outcome: 'Recorded', approval: 'a', user: 'carol', message: 'Recorded: carol approves app-v2 for prod' })),
    pause: vi.fn(async () => ({ message: 'paused' })),
    resume: vi.fn(async () => ({ message: 'resumed' })),
    createBundle: vi.fn(async () => ({ bundle: 'app-v3', message: 'created' })),
    releaseHold: vi.fn(async () => ({ message: 'released' })),
  },
}))

import { App } from './App'

// The lazily loaded views: load their code first, as the page prefetches it.
beforeAll(async () => {
  await Promise.all([import('./stage/StageDrawer'), import('./components/BundleDiffPanel'), import('./components/CommandPalette'),
    import('./components/KeyboardShortcutsPanel'), import('./stationMap/StageTable'), import('./stationMap/VersionMatrix'),
    import('./components/PolicyGatesPanel'), import('./components/PromotionErrorsPanel'), import('./components/ReleaseMetricsBar'), import('./components/PipelineOpsTable')])
})

/** The raw API values that have a different display label in every kind: they must not show. */
const RAW_STATES = (() => {
  const labels = new Set(Object.values(DISPLAY_STATES).flatMap((k) => Object.values(k).map((s) => s.label)))
  const values = new Set(Object.values(DISPLAY_STATES).flatMap((k) => Object.keys(k)))
  return [...values].filter((v) => !labels.has(v)).sort()
})()
const RAW = new RegExp(`\\b(${['PromotionStep', 'PolicyGate', 'Reconciling', 'Bundles?', 'bundles?', ...RAW_STATES].join('|')})\\b`, 'g')

/** Every display label, longest first: a label may contain a raw word ("Waiting for PR merge"). */
const LABELS = [...new Set(Object.values(DISPLAY_STATES).flatMap((k) => Object.values(k).map((s) => s.label)))]
  .sort((a, b) => b.length - a.length)

/**
 * The words a person reads (text and accessible names), outside the Kubernetes details
 * disclosure and other Kubernetes text marked data-k8s (kubectl output), with the display
 * labels taken out.
 */
function readable(root: HTMLElement): string {
  const clone = root.cloneNode(true) as HTMLElement
  clone.querySelectorAll('details.k-k8s, [data-k8s]').forEach((d) => d.remove())
  const names = [...clone.querySelectorAll('[title],[aria-label],[alt],[placeholder]')]
    .flatMap((e) => ['title', 'aria-label', 'alt', 'placeholder'].map((a) => e.getAttribute(a) ?? ''))
  // Text nodes joined by spaces: adjacent elements' textContent runs words together ("3 bundlesRollback").
  const words: string[] = []
  const walk = document.createTreeWalker(clone, NodeFilter.SHOW_TEXT)
  for (let n = walk.nextNode(); n; n = walk.nextNode()) words.push(n.textContent ?? '')
  let text = [...words, ...names].join(' ')
  for (const l of LABELS) text = text.split(l).join(' ')
  return text
}

/** Each raw word on screen now, with the text around it. */
function leaks(): string[] {
  const text = readable(document.body)
  return [...text.matchAll(RAW)].map((m) => text.slice(Math.max(0, m.index! - 30), m.index! + 40).replace(/\s+/g, ' '))
}

async function flush(ms = 60) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
}
async function go(hash: string) {
  window.history.replaceState(null, '', `/ui/${hash}`)
  await act(async () => { window.dispatchEvent(new PopStateEvent('popstate')) })
  await flush()
}
const click = async (el: HTMLElement) => { fireEvent.click(el); await flush() }
const check = (where: string) => expect.soft(leaks(), where).toEqual([])

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  localStorage.clear()
  h.failPipelines = false
  h.noPipelines = false
})
afterEach(() => { cleanup(); vi.useRealTimers() })

describe('vocabulary', () => {
  it('generates the raw states from the display table', () => {
    expect(RAW_STATES).toEqual(expect.arrayContaining(['AbortedByAlarm', 'Block', 'HealthChecking', 'InProgress', 'NotStarted', 'RollingBack', 'WaitingForMerge']))
  })

  it('the fleet, the operations table and every pipeline tab say stage, gate and version', async () => {
    window.history.replaceState(null, '', '/ui/#/')
    render(<App />)
    await flush()
    check('fleet')
    await click(screen.getByRole('button', { name: 'Switch to Operations Table' }))
    check('operations table')
    await click(screen.getByRole('button', { name: 'Switch to List View' }))

    await go('#/p/default/app')
    // Each station's name (its tooltip on the map) and each gate's popover.
    for (const env of ['test', 'uat', 'stage', 'prod', 'dr']) {
      const station = document.querySelector<SVGGElement>(`.station-map [data-station="${env}"]`)!
      fireEvent.focus(station)
      await flush()
      check(`station ${env}`)
    }
    for (const gate of ['soak', 'release-approval', 'business-hours']) {
      const mark = document.querySelector<SVGGElement>(`.station-map__gate[data-gate="${gate}"]`)!
      fireEvent.focus(mark)
      await flush()
      expect(screen.getByRole('tooltip')).toBeInTheDocument()
      check(`gate popover ${gate}`)
      fireEvent.blur(mark)
      await flush(400)
    }
    for (const tab of ['Map', 'Versions', 'Activity', 'Metrics']) {
      await click(screen.getByRole('tab', { name: new RegExp(`^${tab}`) }))
      check(`${tab} tab`)
    }
  })

  it('the stage drawer keeps the kinds under Kubernetes details, for its stage and its gates', async () => {
    // stage has a PromotionStep; prod is held by the soak gate (a PolicyGate) before its step exists.
    for (const [route, kinds] of [['#/p/default/app/env/stage', ['PromotionStep']], ['#/p/default/app/env/prod', ['PolicyGate']]] as const) {
      window.history.replaceState(null, '', `/ui/${route}`)
      const { unmount } = render(<App />)
      await flush()
      const drawer = screen.getByTestId('stage-drawer')
      check(route)
      const k8s = [...drawer.querySelectorAll('details.k-k8s')]
      expect(k8s.map((d) => d.querySelector('dd')?.textContent), route).toEqual(kinds)
      expect(k8s.some((d) => d.hasAttribute('open')), `${route}: collapsed`).toBe(false)
      unmount()
    }
  })

  it('every dialog and the message after it', async () => {
    window.history.replaceState(null, '', '/ui/#/p/default/app')
    render(<App />)
    await flush()

    // Roll back (a Verified stage), then its message: the map offers it under the station in focus.
    fireEvent.focus(document.querySelector<SVGGElement>('.station-map [data-station="test"]')!)
    await flush()
    await click(screen.getAllByRole('button', { name: 'Roll back test' })[0])
    check('roll back dialog')
    await click(within(screen.getByRole('dialog')).getByRole('checkbox', { name: /^Hold test/ }))
    check('roll back and hold')
    await click(within(screen.getByRole('dialog')).getByRole('checkbox', { name: /^Hold test/ }))
    await click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Roll back test' }))
    check('after roll back')

    // Promote (a stage stopped by an alarm whose upstream is live), then its message.
    fireEvent.focus(document.querySelector<SVGGElement>('.station-map [data-station="dr"]')!)
    await flush()
    await click(screen.getAllByRole('button', { name: 'Promote to dr' })[0])
    check('promote dialog')
    await click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Promote to dr' }))
    check('after promote')

    // Pause, ship and compare.
    await click(screen.getByRole('button', { name: 'Pause pipeline' }))
    check('pause dialog')
    await click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    await click(screen.getByRole('button', { name: 'Ship a version of app' }))
    check('ship dialog')
    await click(screen.getByRole('button', { name: 'Cancel shipping a version' }))
    fireEvent.click(screen.getByTitle(/^app-v1: /), { shiftKey: true })
    await flush()
    await click(screen.getByRole('button', { name: /^Compare/ }))
    check('compare dialog')
    fireEvent.keyDown(document, { key: 'Escape' })
    await flush()

    // The approvals list: approve, then the reject confirmation.
    await click(screen.getByRole('tab', { name: /^Activity/ }))
    check('approvals')
    await click(screen.getByRole('button', { name: 'Approve' }))
    check('after approve')
    await click(screen.getByRole('button', { name: 'Reject' }))
    check('reject dialog')
    fireEvent.keyDown(document, { key: 'Escape' })
    await flush()

    // Help and the palette.
    await click(screen.getByRole('button', { name: 'Help and keyboard shortcuts' }))
    check('help')
    fireEvent.keyDown(document, { key: 'Escape' })
    await flush()
    await click(screen.getByRole('button', { name: /^Go to or do/ }))
    check('palette')
  })

  it('an empty pipeline, an empty cluster and a failed read', async () => {
    window.history.replaceState(null, '', '/ui/#/p/default/empty')
    const { unmount } = render(<App />)
    await flush()
    expect(screen.getByTestId('first-version')).toBeInTheDocument()
    check('empty pipeline')
    unmount()

    h.noPipelines = true
    window.history.replaceState(null, '', '/ui/#/')
    const second = render(<App />)
    await flush()
    check('empty cluster')
    second.unmount()

    h.noPipelines = false
    h.failPipelines = true
    render(<App />)
    await flush()
    expect(screen.getAllByRole('button', { name: 'Retry' }).length).toBeGreaterThan(0)
    check('failed read')
  })
})
