// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// App.test.tsx — App-level orchestration: which bundle is shown, how polls and
// pipeline switches interact, and what the header and gates panel show.
// The API client is mocked with the shapes the Go handlers return.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, act, fireEvent, within } from '@testing-library/react'
import type { Bundle, GraphResponse, Pipeline, PolicyGate } from './types'

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
    // Optional graph returned instead of graphFor(bundle) (blocked-banner tests).
    graph: undefined as unknown,
    // When set, listPipelines fails with this error (the API refuses the page).
    pipelinesError: undefined as string | undefined,
    // When set, getGraph fails with this error.
    graphError: undefined as string | undefined,
  }
  const graphFor = (b: string) => ({
    nodes: [{ id: `${b}-step`, type: 'PromotionStep', label: `env-of-${b}`, environment: `env-of-${b}`, state: 'Verified' }],
    edges: [],
  })
  const api = {
    listPipelines: vi.fn(async () => {
      if (state.pipelinesError) throw new Error(state.pipelinesError)
      return state.pipelines
    }),
    listGates: vi.fn(async () => state.gates),
    listBundles: vi.fn(async (p: string) => state.bundles[p] ?? []),
    getGraph: vi.fn(async (b: string) => {
      if (state.graphDelay) await state.graphDelay(b)
      if (state.graphError) throw new Error(state.graphError)
      return state.graph ?? graphFor(b)
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
  h.state.graph = undefined
  h.state.pipelinesError = undefined
  h.state.graphError = undefined
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

  it('reads the graph and steps of the bundle in the namespace on screen', async () => {
    // Bundle names repeat across namespaces; the server only tells them apart
    // by ?namespace=, so every read names the namespace of the pipeline shown.
    window.history.replaceState(null, '', '/ui/#pipeline=app&ns=team-b')
    h.state.pipelines = [
      pipeline('app', { namespace: 'team-a', activeBundleName: 'app-v2' }),
      pipeline('app', { namespace: 'team-b', activeBundleName: 'app-v2' }),
    ]
    h.state.bundles = {
      app: [
        bundle('app-v2', 'Promoting', 1, { namespace: 'team-a' }),
        bundle('app-v2', 'Failed', 1, { namespace: 'team-b' }),
      ],
    }
    render(<App />)
    await flush()
    expect(h.api.getGraph).toHaveBeenLastCalledWith('app-v2', 'team-b')
    expect(h.api.getSteps).toHaveBeenLastCalledWith('app-v2', 'team-b')

    // A poll tick and a timeline pick read the same namespace again.
    await flush(5_100)
    fireEvent.click(screen.getByTitle(/^app-v2: Failed/))
    await flush()
    for (const fn of [h.api.getGraph, h.api.getSteps]) {
      expect(fn.mock.calls.length).toBeGreaterThanOrEqual(3)
      for (const call of fn.mock.calls) expect(call).toEqual(['app-v2', 'team-b'])
    }
  })

  it('opens and closes the node details as Back and Forward change node=', async () => {
    const go = async (hash: string) => {
      window.history.replaceState(null, '', `/ui/${hash}`)
      await act(async () => { window.dispatchEvent(new PopStateEvent('popstate')) })
      await flush()
    }
    window.history.replaceState(null, '', '/ui/#pipeline=app&node=b-new-step')
    render(<App />)
    await flush()
    expect(screen.getByTestId('node-detail')).toBeInTheDocument()

    // Back to the entry before the node was opened.
    await go('#pipeline=app')
    expect(screen.queryByTestId('node-detail')).not.toBeInTheDocument()
    // Forward to it again.
    await go('#pipeline=app&node=b-new-step')
    expect(screen.getByTestId('node-detail')).toBeInTheDocument()
  })
})

// #338: Shift-click picks the bundle to compare; the timeline then offers
// Compare (opens the comparison, bundle= in the URL) and clear.
describe('App bundle comparison', () => {
  const dialog = () => screen.queryByRole('dialog', { name: 'Bundle comparison' })
  const oldChip = () => screen.getByTitle(/^b-old: Superseded/)

  it('leaves Compare and clear reachable after a shift-click, and Compare opens the comparison', async () => {
    render(<App />)
    await flush()
    fireEvent.click(oldChip(), { shiftKey: true })
    await flush()
    expect(oldChip().className).toContain('bundle-chip--compare')
    expect(dialog()).not.toBeInTheDocument()
    expect(window.location.hash).not.toContain('bundle=')

    // Compare opens the comparison and moves focus into it.
    const compare = screen.getByRole('button', { name: 'Compare ↔' })
    compare.focus()
    fireEvent.click(compare)
    await flush()
    expect(dialog()).toBeInTheDocument()
    expect(dialog()).toContainElement(document.activeElement as HTMLElement)
    expect(window.location.hash).toContain('bundle=b-old')

    // Closing it returns focus to Compare, which opens it again.
    fireEvent.click(within(dialog()!).getByRole('button', { name: 'Close' }))
    await flush()
    expect(dialog()).not.toBeInTheDocument()
    expect(window.location.hash).not.toContain('bundle=')
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Compare ↔' }))
    fireEvent.click(screen.getByRole('button', { name: 'Compare ↔' }))
    await flush()
    fireEvent.keyDown(document, { key: 'Escape' })
    await flush()
    expect(dialog()).not.toBeInTheDocument()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Compare ↔' }))

    // clear drops the comparison bundle.
    fireEvent.click(screen.getByRole('button', { name: '× clear' }))
    await flush()
    expect(oldChip().className).not.toContain('bundle-chip--compare')
    expect(screen.queryByRole('button', { name: 'Compare ↔' })).not.toBeInTheDocument()
    expect(screen.getByText('Shift-click to compare')).toBeInTheDocument()
  })

  it('opens the comparison from a bundle= link', async () => {
    window.history.replaceState(null, '', '/ui/#pipeline=app&bundle=b-old')
    render(<App />)
    await flush()
    expect(dialog()).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'Escape' })
    await flush()
    expect(dialog()).not.toBeInTheDocument()
    expect(window.location.hash).not.toContain('bundle=')
    // The linked bundle stays picked, as after Compare.
    expect(oldChip().className).toContain('bundle-chip--compare')
    expect(screen.getByRole('button', { name: 'Compare ↔' })).toBeInTheDocument()
  })

  // The shown bundle is Bundle A: it is never also Bundle B.
  it('does not compare the shown bundle with itself', async () => {
    const newChip = () => screen.getByTitle(/^b-new: Promoting/)
    const picked = () => screen.queryAllByTitle(/^b-(new|old): /).filter(c => c.className.includes('bundle-chip--compare'))
    render(<App />)
    await flush()

    // A shift-click on the shown bundle does nothing.
    fireEvent.click(newChip(), { shiftKey: true })
    await flush()
    expect(picked()).toHaveLength(0)
    expect(screen.queryByRole('button', { name: '× clear' })).not.toBeInTheDocument()
    expect(screen.getByText('Shift-click to compare')).toBeInTheDocument()
    expect(newChip().getAttribute('title')).toBe('b-new: Promoting')

    // Showing Bundle B makes it Bundle A, and drops it as B.
    fireEvent.click(oldChip(), { shiftKey: true })
    await flush()
    expect(picked()).toEqual([oldChip()])
    fireEvent.click(oldChip())
    await flush()
    expect(oldChip().className).toContain('bundle-chip--selected')
    expect(picked()).toHaveLength(0)
    expect(screen.queryByRole('button', { name: '× clear' })).not.toBeInTheDocument()
    fireEvent.click(newChip())
    await flush()
    expect(picked()).toHaveLength(0)
  })

  it('does not open a comparison of the shown bundle with itself from a link', async () => {
    window.history.replaceState(null, '', '/ui/#pipeline=app&bundle=b-new')
    render(<App />)
    await flush()
    expect(screen.getByTitle(/^b-new: Promoting/).className).toContain('bundle-chip--selected')
    expect(dialog()).not.toBeInTheDocument()
  })
})

// docs/installation.md: a NodePort without TLS shows a security warning. The
// API refuses such a client outright unless ui.allowedHosts names the host, so
// the warning cannot wait for a pipeline view: it heads every view.
describe('App insecure connection banner', () => {
  const original = window.location
  const at = (href: string) => Object.defineProperty(window, 'location', { value: new URL(href), writable: true, configurable: true })
  afterEach(() => {
    Object.defineProperty(window, 'location', { value: original, writable: true, configurable: true })
  })
  const banners = () => screen.queryAllByText(/^Insecure connection — kardinal UI is accessed over plain HTTP\./)

  it('warns on the landing page', async () => {
    at('http://10.0.0.1:30082/ui/')
    render(<App />)
    await flush()
    expect(screen.getByRole('heading', { level: 2, name: 'Fleet' })).toBeInTheDocument()
    expect(banners()).toHaveLength(1)
  })

  it('warns when the API refuses the page and no pipeline loads', async () => {
    at('http://10.0.0.1:30082/ui/')
    h.state.pipelinesError = 'API error 403: Forbidden'
    render(<App />)
    await flush()
    expect(h.api.getGraph).not.toHaveBeenCalled()
    expect(banners()).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss insecure connection warning' }))
    expect(banners()).toHaveLength(0)
  })

  it('warns once in the pipeline view and in the operations table', async () => {
    at('http://kardinal.internal:8082/ui/#pipeline=app')
    render(<App />)
    await flush()
    expect(screen.getByRole('heading', { level: 1, name: 'app' })).toBeInTheDocument()
    expect(banners()).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: 'Switch to Operations Table' }))
    await flush()
    expect(banners()).toHaveLength(1)
  })

  it('does not warn on the documented port-forward', async () => {
    at('http://127.0.0.1:8082/ui/')
    render(<App />)
    await flush()
    expect(screen.getByRole('heading', { level: 2, name: 'Fleet' })).toBeInTheDocument()
    expect(banners()).toHaveLength(0)
  })
})

// The API client throws Error('API error 503: ...'); the views print the
// message after "Error: ", so it reads once, not "Error: Error: ...".
describe('App failed reads', () => {
  it('shows the message of a failed pipelines read once', async () => {
    h.state.pipelinesError = 'API error 503: Service Unavailable'
    render(<App />)
    await flush()
    expect(screen.getByText('Error: API error 503: Service Unavailable')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh data' }).querySelector('span[aria-live="polite"]'))
      .toHaveAttribute('title', 'Error: API error 503: Service Unavailable')
    expect(screen.queryByText(/Error: Error/)).not.toBeInTheDocument()
  })

  it('keeps the sidebar list while a pipelines poll fails, and drops the error when one works', async () => {
    h.state.pipelines = [pipeline('app', { activeBundleName: 'b-new' }), pipeline('other')]
    render(<App />)
    await flush()
    const list = () => screen.getByRole('list', { name: 'Pipelines' })
    expect(within(list()).getByText('other')).toBeInTheDocument()

    h.state.pipelinesError = 'API error 503: Service Unavailable'
    await flush(5_100)
    expect(screen.getByText('Error: API error 503: Service Unavailable')).toBeInTheDocument()
    expect(within(list()).getByText('other')).toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Filter pipelines by name or namespace' })).toBeInTheDocument()

    h.state.pipelinesError = undefined
    await flush(5_100)
    expect(screen.queryByText(/^Error: /)).not.toBeInTheDocument()
    expect(within(list()).getByText('other')).toBeInTheDocument()
  })

  it('shows the message of a failed graph read once', async () => {
    h.state.graphError = 'API error 500: Internal Server Error'
    render(<App />)
    await flush()
    expect(screen.getByRole('alert')).toHaveTextContent(/^Error: API error 500: Internal Server Error$/)
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

  // #766, docs/changelog.md: the indicator is amber after 15 s and red after
  // 30 s. A failing API is how data gets that old, so an error does not keep
  // it amber.
  it('turns the stale-data indicator red after 30 s, also while the polls fail', async () => {
    render(<App />)
    await flush()
    const indicator = () => screen.getByRole('button', { name: 'Refresh data' }).querySelector<HTMLElement>('span[aria-live="polite"]')!
    expect(indicator().style.color).toBe('var(--color-text-secondary)')

    h.state.pipelinesError = 'API error 503: Service Unavailable'
    await flush(5_100)
    expect(indicator().style.color).toBe('var(--color-warning)')
    await flush(11_000)
    expect(indicator().textContent).toMatch(/^⚠ 1\ds ago$/)
    expect(indicator().style.color).toBe('var(--color-warning)')
    await flush(15_000)
    expect(indicator().textContent).toMatch(/^⚠ 3\ds ago$/)
    expect(indicator().style.color).toBe('var(--color-error)')
    expect(indicator().style.animation).toContain('stalePulse')
    expect(indicator()).toHaveAttribute('title', 'Error: API error 503: Service Unavailable')
  })
})

describe('App policy gates panel', () => {
  it('shows only the gates of the shown bundle and ignores templates', async () => {
    h.state.gates = [
      { name: 'no-weekend', namespace: 'default', expression: '!schedule.isWeekend', ready: false, template: true, state: 'Pending' },
      { name: 'no-weekend-b-new-prod', namespace: 'default', expression: '!schedule.isWeekend', ready: true, state: 'Pass', pipeline: 'app', bundle: 'b-new', environment: 'prod' },
      { name: 'other-gate', namespace: 'default', expression: 'true', ready: false, state: 'Block', holding: true, pipeline: 'other', bundle: 'o-1', environment: 'prod' },
    ] as PolicyGate[]
    render(<App />)
    await flush()
    expect(screen.queryByText('1 blocked')).not.toBeInTheDocument()
    expect(screen.getByText('1 passing')).toBeInTheDocument()
    expect(screen.queryByText('other-gate')).not.toBeInTheDocument()
  })
})


// E2E-R19: the banner, "Show blocked" and the gates-panel chip count only the
// gates the UI API marks holding (graph.GateHolds, the rule blockerCount uses).
// A not-ready gate that does not hold the bundle is shown as Waiting.
describe('App blocked banner counts only holding gates (E2E-R19)', () => {
  // The bundle is health-checking in test; the prod soak gate was evaluated
  // not ready. The graph and gates handlers return it as they do on a cluster.
  function gateCase(holding: boolean) {
    const gateNode = {
      id: 'soak-b-new-prod', type: 'PolicyGate', label: 'soak', environment: 'prod',
      state: holding ? 'Block' : 'Waiting', lastEvaluatedAt: new Date().toISOString(),
      ...(holding ? { holding: true } : {}),
    }
    h.state.graph = {
      nodes: [
        { id: 'b-new-test', type: 'PromotionStep', label: 'test', environment: 'test', state: 'HealthChecking' },
        gateNode,
      ],
      edges: [{ from: 'b-new-test', to: 'soak-b-new-prod' }],
    } as GraphResponse
    h.state.gates = [{
      name: 'soak-b-new-prod', namespace: 'default', expression: 'upstream.test.soakMinutes >= 30',
      ready: false, reason: 'soak 0m < 30m', pipeline: 'app', bundle: 'b-new', environment: 'prod',
      state: holding ? 'Block' : 'Waiting', ...(holding ? { holding: true } : {}),
    }] as PolicyGate[]
  }

  it('shows 0 blocked and the gate as waiting when the bundle has not reached the gate', async () => {
    gateCase(false)
    render(<App />)
    await flush()
    expect(screen.queryByText(/blocking promotion/)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Show blocked' })).not.toBeInTheDocument()
    expect(screen.queryByText(/\d+ blocked/)).not.toBeInTheDocument()
    expect(screen.getByText('1 waiting')).toBeInTheDocument()
    expect(screen.getByLabelText('soak — Waiting')).toBeInTheDocument()
  })

  it('shows 0 blocked for a Failed bundle', async () => {
    h.state.bundles = { app: [bundle('b-new', 'Failed', 1)] }
    gateCase(false)
    render(<App />)
    await flush()
    expect(screen.queryByText(/blocking promotion/)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Show blocked' })).not.toBeInTheDocument()
    expect(screen.queryByText(/\d+ blocked/)).not.toBeInTheDocument()
  })

  it('shows 1 blocked when the gate holds the bundle', async () => {
    gateCase(true)
    render(<App />)
    await flush()
    expect(screen.getByText('1 PolicyGate blocking promotion')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Show blocked' })).toBeInTheDocument()
    expect(screen.getByText('1 blocked')).toBeInTheDocument()
  })
})
