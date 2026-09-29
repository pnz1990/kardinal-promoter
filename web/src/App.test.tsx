// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// App.test.tsx — App-level orchestration: which bundle is shown, how polls and
// pipeline switches interact, and what the header and gates panel show.
// The API client is mocked with the shapes the Go handlers return.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, act, fireEvent, within } from '@testing-library/react'
import type { Bundle, Pipeline, PolicyGate } from './types'

vi.mock('@dagrejs/dagre', () => {
  function MockGraph(this: Record<string, unknown>) {
    this.setGraph = vi.fn(); this.setDefaultEdgeLabel = vi.fn(); this.setNode = vi.fn(); this.setEdge = vi.fn()
    this.node = vi.fn().mockReturnValue({ x: 100, y: 80 })
  }
  return { default: { graphlib: { Graph: MockGraph }, layout: vi.fn() } }
})

const h = vi.hoisted(() => {
  const state = {
    pipelines: [] as unknown[],
    bundles: {} as Record<string, unknown[]>,
    gates: [] as unknown[],
    // Optional hook to delay a getGraph response (stale-response tests).
    graphDelay: undefined as undefined | ((bundle: string) => Promise<void>),
  }
  const graphFor = (b: string) => ({
    nodes: [{ id: `${b}-step`, type: 'PromotionStep', label: `env-of-${b}`, environment: `env-of-${b}`, state: 'Verified' }],
    edges: [],
  })
  const api = {
    listPipelines: vi.fn(async () => state.pipelines),
    listGates: vi.fn(async () => state.gates),
    listBundles: vi.fn(async (p: string) => state.bundles[p] ?? []),
    getGraph: vi.fn(async (b: string) => {
      if (state.graphDelay) await state.graphDelay(b)
      return graphFor(b)
    }),
    getSteps: vi.fn(async () => []),
    getStepEvents: vi.fn(async () => []),
    validateCEL: vi.fn(async () => ({ valid: true })),
    promote: vi.fn(async () => ({ bundle: 'x', message: 'ok' })),
    rollback: vi.fn(async () => ({ bundle: 'x', message: 'ok' })),
    pause: vi.fn(async () => ({ message: 'ok' })),
    resume: vi.fn(async () => ({ message: 'ok' })),
    createBundle: vi.fn(async () => ({ bundle: 'x', message: 'ok' })),
  }
  return { state, api }
})

vi.mock('./api/client', () => ({ api: h.api }))

import { App } from './App'

const day = 86_400_000
function bundle(name: string, phase: string, ageDays: number, extra: Partial<Bundle> = {}): Bundle {
  return {
    name, namespace: 'default', phase, type: 'image', pipeline: 'app',
    createdAt: new Date(Date.now() - ageDays * day).toISOString(), ...extra,
  }
}
function pipeline(name: string, extra: Partial<Pipeline> = {}): Pipeline {
  return { name, namespace: 'default', phase: 'Ready', environmentCount: 1, ...extra }
}

async function flush(ms = 50) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
}
const lastGraphCall = () => h.api.getGraph.mock.calls.at(-1)?.[0]

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  h.state.pipelines = [pipeline('app', { phase: 'Ready', activeBundleName: 'b-new' })]
  h.state.bundles = { app: [bundle('b-new', 'Promoting', 1), bundle('b-old', 'Superseded', 2)] }
  h.state.gates = []
  h.state.graphDelay = undefined
  for (const fn of Object.values(h.api)) fn.mockClear()
  localStorage.clear()
  window.history.replaceState(null, '', '/ui/#pipeline=app')
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('App bundle selection', () => {
  it('keeps the timeline-selected bundle across polls', async () => {
    render(<App />)
    await flush()
    expect(screen.getAllByText('env-of-b-new').length).toBeGreaterThan(0)

    fireEvent.click(screen.getByTitle(/^b-old: Superseded/))
    await flush()
    expect(screen.getAllByText('env-of-b-old').length).toBeGreaterThan(0)

    // Two poll ticks.
    await flush(5_100)
    await flush(5_100)
    expect(lastGraphCall()).toBe('b-old')
    expect(screen.getAllByText('env-of-b-old').length).toBeGreaterThan(0)
    expect(screen.queryAllByText('env-of-b-new')).toHaveLength(0)
    expect(screen.getByTitle(/^b-old: Superseded/).className).toContain('bundle-chip--selected')
    expect(screen.getByTitle(/^b-new: Promoting/).className).not.toContain('bundle-chip--selected')
  })

  it('falls back to the default bundle when the selected bundle disappears', async () => {
    render(<App />)
    await flush()
    fireEvent.click(screen.getByTitle(/^b-old: Superseded/))
    await flush()
    expect(lastGraphCall()).toBe('b-old')

    h.state.bundles = { app: [bundle('b-new', 'Promoting', 1)] }
    await flush(5_100)
    expect(lastGraphCall()).toBe('b-new')
    expect(screen.getAllByText('env-of-b-new').length).toBeGreaterThan(0)
    expect(screen.getByTitle(/^b-new: Promoting/).className).toContain('bundle-chip--selected')
  })

  it('shows the pipeline active bundle, not the first bundle in API order', async () => {
    // The API returns bundles in no particular order; nothing is Promoting.
    h.state.bundles = { app: [bundle('b-old', 'Superseded', 2), bundle('b-new', 'Verified', 1)] }
    render(<App />)
    await flush()
    expect(lastGraphCall()).toBe('b-new')
    // Reversing the order on the next poll does not move the DAG.
    h.state.bundles = { app: [bundle('b-new', 'Verified', 1), bundle('b-old', 'Superseded', 2)] }
    await flush(5_100)
    expect(new Set(h.api.getGraph.mock.calls.map(c => c[0]))).toEqual(new Set(['b-new']))
  })

  it('drops a slow graph response for a pipeline the user has left', async () => {
    window.history.replaceState(null, '', '/ui/')
    h.state.pipelines = [pipeline('alpha', { activeBundleName: 'a-1' }), pipeline('beta', { activeBundleName: 'b-1' })]
    h.state.bundles = {
      alpha: [bundle('a-1', 'Promoting', 1, { pipeline: 'alpha' })],
      beta: [bundle('b-1', 'Promoting', 1, { pipeline: 'beta' })],
    }
    let releaseAlpha: () => void = () => {}
    const alphaGate = new Promise<void>(r => { releaseAlpha = r })
    h.state.graphDelay = async (b: string) => { if (b === 'a-1') await alphaGate }

    render(<App />)
    await flush()
    const list = screen.getByRole('list', { name: 'Pipelines' })
    fireEvent.click(within(list).getByText('alpha').closest('button')!)
    await flush()
    fireEvent.click(within(list).getByText('beta').closest('button')!)
    await flush()
    expect(screen.getAllByText('env-of-b-1').length).toBeGreaterThan(0)

    releaseAlpha()
    await flush()
    expect(screen.queryAllByText('env-of-a-1')).toHaveLength(0)
    expect(screen.getAllByText('env-of-b-1').length).toBeGreaterThan(0)
  })

  it('does not start a new graph poll while the previous one is still running', async () => {
    let release: () => void = () => {}
    const gate = new Promise<void>(r => { release = r })
    render(<App />)
    await flush()
    const before = h.api.getGraph.mock.calls.length
    h.state.graphDelay = async () => { await gate }
    await flush(5_100) // tick starts and hangs
    await flush(5_100) // tick must be skipped
    await flush(5_100)
    expect(h.api.getGraph.mock.calls.length).toBe(before + 1)
    release()
    await flush()
  })
})

describe('App pipeline selection', () => {
  it('pushes a single history entry when a pipeline is selected', async () => {
    // A node is open on the current pipeline; switching pipelines must clear it
    // in the same history entry, so one Back press returns to the old view.
    window.history.replaceState(null, '', '/ui/#pipeline=app&node=b-new-step')
    h.state.pipelines = [pipeline('app', { activeBundleName: 'b-new' }), pipeline('beta', { activeBundleName: 'x-1' })]
    h.state.bundles.beta = [bundle('x-1', 'Promoting', 1, { pipeline: 'beta' })]
    render(<App />)
    await flush()
    const push = vi.spyOn(window.history, 'pushState')
    const list = screen.getByRole('list', { name: 'Pipelines' })
    fireEvent.click(within(list).getByText('beta').closest('button')!)
    await flush()
    expect(push).toHaveBeenCalledTimes(1)
    expect(window.location.hash).toBe('#pipeline=beta&ns=default')
    push.mockRestore()
  })

  it('tells same-named pipelines in two namespaces apart', async () => {
    window.history.replaceState(null, '', '/ui/')
    h.state.pipelines = [
      pipeline('app', { namespace: 'team-a', activeBundleName: 'app-a', paused: false }),
      pipeline('app', { namespace: 'team-b', activeBundleName: 'app-b', paused: true }),
    ]
    h.state.bundles = {
      app: [
        bundle('app-a', 'Promoting', 1, { namespace: 'team-a' }),
        bundle('app-b', 'Promoting', 2, { namespace: 'team-b' }),
      ],
    }
    render(<App />)
    await flush()
    const list = screen.getByRole('list', { name: 'Pipelines' })
    const buttons = within(list).getAllByText('app').map(el => el.closest('button')!)
    expect(buttons).toHaveLength(2)
    // team-b is listed second (namespaces sort alphabetically).
    fireEvent.click(buttons[1])
    await flush()
    expect(buttons[1]).toHaveAttribute('aria-pressed', 'true')
    expect(buttons[0]).toHaveAttribute('aria-pressed', 'false')
    expect(lastGraphCall()).toBe('app-b')
    expect(screen.getByText(/PAUSED/, { selector: 'main span' })).toBeInTheDocument()
    expect(screen.getByTitle('Namespace: team-b')).toBeInTheDocument()
    // The timeline only lists bundles from the selected namespace.
    expect(screen.queryByTitle(/^app-a:/)).not.toBeInTheDocument()
  })
})

describe('App header', () => {
  it('loads the logo from the /ui/ base path', async () => {
    // Vitest serves from '/'; the production build sets base '/ui/' (vite.config.ts).
    vi.stubEnv('BASE_URL', '/ui/')
    render(<App />)
    await flush()
    expect(screen.getByAltText('Kardinal')).toHaveAttribute('src', '/ui/logo.png')
  })
})

describe('App policy gates panel', () => {
  it('shows only the gates of the shown bundle and ignores templates', async () => {
    h.state.gates = [
      { name: 'no-weekend', namespace: 'default', expression: '!schedule.isWeekend', ready: false, template: true },
      { name: 'no-weekend-b-new-prod', namespace: 'default', expression: '!schedule.isWeekend', ready: true, pipeline: 'app', bundle: 'b-new', environment: 'prod' },
      { name: 'other-gate', namespace: 'default', expression: 'true', ready: false, pipeline: 'other', bundle: 'o-1', environment: 'prod' },
    ] as PolicyGate[]
    render(<App />)
    await flush()
    expect(screen.queryByText('1 blocked')).not.toBeInTheDocument()
    expect(screen.getByText('1 passing')).toBeInTheDocument()
    expect(screen.queryByText('other-gate')).not.toBeInTheDocument()
  })
})

