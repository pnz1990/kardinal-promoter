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

// NodeDetail.test.tsx — Tests for the node detail panel (#533).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, within, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { NodeDetail } from './NodeDetail'
import type { GraphEdge, GraphNode, PromotionStep } from '../types'

// Mock the API client — NodeDetail calls validateCEL for PolicyGate nodes
vi.mock('../api/client', () => ({
  api: {
    validateCEL: vi.fn().mockResolvedValue({ valid: true }),
    promote: vi.fn().mockResolvedValue({ bundle: 'b', message: 'ok' }),
    rollback: vi.fn().mockResolvedValue({ bundle: 'b', message: 'ok' }),
    getSteps: vi.fn().mockResolvedValue([]),
    getStepEvents: vi.fn().mockResolvedValue([]),
  },
}))

const makePromotionStepNode = (overrides: Partial<GraphNode> = {}): GraphNode => ({
  id: 'step-test',
  type: 'PromotionStep',
  label: 'test',
  environment: 'test',
  state: 'Promoting',
  ...overrides,
})

const makePolicyGateNode = (overrides: Partial<GraphNode> = {}): GraphNode => ({
  id: 'gate-wk',
  type: 'PolicyGate',
  label: 'no-weekend',
  environment: 'no-weekend',
  state: 'Block',
  expression: '!schedule.isWeekend',
  ...overrides,
})

const makeStep = (overrides: Partial<PromotionStep> = {}): PromotionStep => ({
  name: 'step-test-bundle',
  namespace: 'default',
  pipeline: 'my-app',
  bundle: 'bundle-abc',
  environment: 'test',
  stepType: 'kustomize-set-image',
  state: 'Promoting',
  ...overrides,
})

describe('NodeDetail — null node', () => {
  it('renders nothing when node=null', () => {
    const { container } = render(<NodeDetail node={null} onClose={vi.fn()} />)
    expect(container.firstChild).toBeNull()
  })
})

describe('NodeDetail — skeleton loading state (#784)', () => {
  // The skeleton is shown when stepLoading=true — which happens between the
  // useEffect setting stepLoading=true and getSteps resolving.
  // We verify by checking that the OLD italic text "Loading step details..." is gone:
  // the component now renders a skeleton bar instead of text.
  // Because getSteps resolves immediately in tests (mockResolvedValue([])),
  // the skeleton passes through quickly. We test the static output when steps
  // prop IS provided (no loading), and verify the obsolete italic text path
  // is no longer present in the component source (regression guard).
  it('no longer renders italic "Loading step details..." text (#784)', () => {
    // With steps prop provided, stepLoading is never true; render is direct.
    const steps = [makeStep({ environment: 'test' })]
    const { container } = render(
      <NodeDetail
        node={makePromotionStepNode({ environment: 'test' })}
        onClose={vi.fn()}
        steps={steps}
      />
    )
    // Old italic text path must not exist anywhere — skeleton replaced it
    const italicDiv = Array.from(container.querySelectorAll('div'))
      .find(el => el.style.fontStyle === 'italic' && /Loading step details/i.test(el.textContent ?? ''))
    expect(italicDiv).toBeUndefined()
  })
})

describe('NodeDetail — close button', () => {
  it('calls onClose when close button is clicked', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    render(
      <NodeDetail node={makePromotionStepNode()} onClose={onClose} />
    )
    const closeBtn = screen.getByLabelText('Close')
    await user.click(closeBtn)
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

describe('NodeDetail — PromotionStep node', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders environment name', () => {
    render(
      <NodeDetail
        node={makePromotionStepNode({ environment: 'production', state: 'Verified' })}
        onClose={vi.fn()}
      />
    )
    expect(screen.getByText('production')).toBeInTheDocument()
  })

  it('renders HealthChip with node state', () => {
    render(
      <NodeDetail
        node={makePromotionStepNode({ state: 'Verified' })}
        onClose={vi.fn()}
      />
    )
    // HealthChip renders the state label
    expect(screen.getByText('Verified')).toBeInTheDocument()
  })

  it('shows PR link when prURL is provided', () => {
    render(
      <NodeDetail
        node={makePromotionStepNode({ prURL: 'https://github.com/org/repo/pull/42' })}
        onClose={vi.fn()}
      />
    )
    // PR link text is "View Pull Request ↗" for non-WaitingForMerge states
    expect(screen.getByRole('link', { name: /View Pull Request/i })).toBeInTheDocument()
  })

  it('shows conditions section when step has conditions', () => {
    const steps = [makeStep({
      environment: 'test',
      conditions: [
        { type: 'Ready', status: 'True', message: 'Step complete' },
      ],
    })]
    render(
      <NodeDetail
        node={makePromotionStepNode({ id: 'step-test', environment: 'test' })}
        onClose={vi.fn()}
        steps={steps}
      />
    )
    expect(screen.getByText('Ready')).toBeInTheDocument()
  })
})

describe('NodeDetail — PolicyGate node', () => {
  it('renders gate name', () => {
    render(
      <NodeDetail
        node={makePolicyGateNode({ label: 'no-weekend-deploys' })}
        onClose={vi.fn()}
      />
    )
    expect(screen.getByText(/no-weekend-deploys/i)).toBeInTheDocument()
  })

  it('renders CEL expression', () => {
    render(
      <NodeDetail
        node={makePolicyGateNode({ expression: '!schedule.isWeekend' })}
        onClose={vi.fn()}
      />
    )
    // Expression appears in the syntax-highlighted code block
    expect(screen.getByText(/isWeekend/i)).toBeInTheDocument()
  })
})

describe('NodeDetail — skeleton loading state (#784)', () => {
  it('shows data-testid=step-skeleton instead of "Loading step details..." text', async () => {
    // getSteps returns a never-resolving promise so stepLoading stays true during render
    const { api } = await import('../api/client')
    ;(api.getSteps as ReturnType<typeof vi.fn>).mockReturnValue(new Promise<PromotionStep[]>(() => {}))

    const { getByTestId, queryByText } = render(
      <NodeDetail
        node={makePromotionStepNode()}
        onClose={vi.fn()}
        bundleName="test-bundle"
      />
    )

    // Skeleton placeholder should be present while loading
    const { waitFor: localWait } = await import('@testing-library/react')
    await localWait(() => {
      expect(getByTestId('step-skeleton')).toBeDefined()
    })
    // Old italic text must not appear
    expect(queryByText('Loading step details...')).toBeNull()
  })
})

// ── Audit fixes ─────────────────────────────────────────────────────────────

describe('NodeDetail — step sequence from status.steps[] (C10b-web-07)', () => {
  const stepNames = () =>
    within(screen.getByRole('list', { name: 'Promotion steps' }))
      .getAllByRole('listitem')
      .map(li => li.querySelector('span:nth-of-type(2)')?.textContent)

  it.each([
    {
      name: 'argocd sequence',
      step: makeStep({
        state: 'HealthChecking',
        stepType: 'argocd-set-image',
        currentStepIndex: 1,
        steps: [
          { name: 'argocd-set-image', state: 'Completed', durationMs: 2000 },
          { name: 'health-check', state: 'InProgress' },
        ],
      }),
      want: ['argocd-set-image', 'health-check'],
      current: 'health-check',
      note: 'checking health',
    },
    {
      name: 'verifying after the health check',
      step: makeStep({
        state: 'Verifying',
        stepType: 'kustomize-set-image',
        currentStepIndex: 1,
        steps: [
          { name: 'git-push', state: 'Completed' },
          { name: 'health-check', state: 'InProgress' },
        ],
      }),
      want: ['git-push', 'health-check'],
      current: 'health-check',
      note: 'verifying: post-deploy hooks and analyses',
    },
    {
      name: 'kustomize sequence at git-commit',
      step: makeStep({
        state: 'Promoting',
        stepType: 'kustomize-set-image',
        currentStepIndex: 2,
        steps: [
          { name: 'git-clone', state: 'Completed' },
          { name: 'kustomize-set-image', state: 'Completed' },
          { name: 'git-commit', state: 'InProgress' },
          { name: 'git-push', state: 'Pending' },
          { name: 'health-check', state: 'Pending' },
        ],
      }),
      want: ['git-clone', 'kustomize-set-image', 'git-commit', 'git-push', 'health-check'],
      current: 'git-commit',
      note: null,
    },
  ])('$name: shows the real steps and marks the running one', ({ step, want, current, note }) => {
    render(<NodeDetail node={makePromotionStepNode()} onClose={vi.fn()} steps={[step]} />)
    expect(stepNames()).toEqual(want)
    expect(screen.queryByText('helm-set-image')).toBeNull()
    const running = screen.getByText(current).closest('li')!
    expect(running).toHaveAttribute('data-step-state', 'InProgress')
    expect(within(running).getByText('running')).toBeInTheDocument()
    if (note) expect(within(running).getByText(note)).toBeInTheDocument()
  })

  it('shows a step the engine left running as failed once the promotion failed', () => {
    const step = makeStep({
      state: 'Failed',
      message: 'health check timeout after 10m0s',
      steps: [
        { name: 'argocd-set-image', state: 'Completed' },
        { name: 'health-check', state: 'InProgress' },
      ],
    })
    render(<NodeDetail node={makePromotionStepNode({ state: 'Failed' })} onClose={vi.fn()} steps={[step]} />)
    const li = screen.getByText('health-check').closest('li')!
    expect(li).toHaveAttribute('data-step-state', 'Failed')
    expect(within(li).getByText('health check timeout after 10m0s')).toBeInTheDocument()
  })

  it('says when the controller has not reported steps yet', () => {
    render(<NodeDetail node={makePromotionStepNode()} onClose={vi.fn()} steps={[makeStep({ state: 'Pending' })]} />)
    expect(screen.getByText('Steps appear here once this promotion starts.')).toBeInTheDocument()
  })
})

describe('NodeDetail — elapsed timer (C10b-web-25)', () => {
  it('counts from node.startedAt, the field Go sets on step nodes', () => {
    const startedAt = new Date(Date.now() - 125_000).toISOString()
    render(
      <NodeDetail
        node={makePromotionStepNode({ startedAt })}
        onClose={vi.fn()}
        steps={[makeStep({ state: 'Promoting' })]}
      />,
    )
    expect(screen.getByText(/^Elapsed:/).parentElement).toHaveTextContent(/Elapsed:\s*2m \d+s/)
  })

  // #1365: RollingBack is an end state (the rollback Bundle's step is the
  // one in flight), so its node shows no running timer, as the CLI shows it.
  it('shows no timer for a RollingBack step', () => {
    const startedAt = new Date(Date.now() - 125_000).toISOString()
    render(
      <NodeDetail
        node={makePromotionStepNode({ startedAt, state: 'RollingBack' })}
        onClose={vi.fn()}
        steps={[makeStep({ state: 'RollingBack' })]}
      />,
    )
    expect(screen.queryByText(/^Elapsed:/)).toBeNull()
  })
})

// uat → gate → prod, the shape the graph API sends. Promote and Roll back follow
// pipelineActions.ts (the same rule as the lane).
function prodGraph(uat: string, prod: string) {
  const nodes: GraphNode[] = [
    makePromotionStepNode({ id: 'step-uat', label: 'uat', environment: 'uat', state: uat }),
    makePolicyGateNode({ id: 'gate-prod', state: 'Pass' }),
    makePromotionStepNode({ id: 'step-prod', label: 'prod', environment: 'prod', state: prod }),
  ]
  const edges: GraphEdge[] = [
    { from: 'step-uat', to: 'gate-prod' },
    { from: 'gate-prod', to: 'step-prod' },
  ]
  return { nodes, edges }
}

function renderProd(uat: string, prod: string, opts: { onActionDone?: () => void; clicked?: string; withGraph?: boolean } = {}) {
  const { nodes, edges } = prodGraph(uat, prod)
  const onActionDone = opts.onActionDone ?? vi.fn()
  const graphProps = opts.withGraph === false ? {} : { nodes, edges }
  const utils = render(
    <NodeDetail
      // The selected node is the copy from the click; the graph has the current state.
      node={makePromotionStepNode({ id: 'step-prod', label: 'prod', environment: 'prod', state: opts.clicked ?? prod })}
      onClose={vi.fn()}
      pipelineName="my-app"
      namespace="team-a"
      steps={[makeStep({ environment: 'prod', state: prod })]}
      onActionDone={onActionDone}
      {...graphProps}
    />,
  )
  return { onActionDone, ...utils, nodes, edges }
}

const buttonNames = () =>
  screen.queryAllByRole('button', { name: /^(Promote to|Roll back) prod$/ }).map(b => b.textContent)

describe('NodeDetail — Promote and Roll back only where they can act (C10b-web-08)', () => {
  it.each([
    { name: 'not reached yet, upstream verified', uat: 'Verified', prod: 'NotStarted', want: ['▶Promote to prod'] },
    { name: 'failed, upstream verified', uat: 'Verified', prod: 'Failed', want: ['▶Promote to prod'] },
    { name: 'stopped by an alarm, upstream verified', uat: 'Verified', prod: 'AbortedByAlarm', want: ['▶Promote to prod'] },
    { name: 'verified', uat: 'Verified', prod: 'Verified', want: ['↩Roll back prod'] },
    { name: 'promoting', uat: 'Verified', prod: 'Promoting', want: [] },
    { name: 'waiting for merge', uat: 'Verified', prod: 'WaitingForMerge', want: [] },
    { name: 'health checking', uat: 'Verified', prod: 'HealthChecking', want: [] },
    { name: 'rolling back', uat: 'Verified', prod: 'RollingBack', want: [] },
    { name: 'not reached yet, upstream still promoting', uat: 'Promoting', prod: 'NotStarted', want: [] },
    { name: 'failed, upstream failed too', uat: 'Failed', prod: 'Failed', want: [] },
  ])('$name → $want', ({ uat, prod, want }) => {
    renderProd(uat, prod)
    expect(buttonNames()).toEqual(want)
  })

  it('offers no Promote on the first environment (nothing upstream)', () => {
    render(
      <NodeDetail
        node={makePromotionStepNode({ id: 'step-test', environment: 'test', state: 'Failed' })}
        onClose={vi.fn()}
        pipelineName="my-app"
        nodes={[makePromotionStepNode({ id: 'step-test', environment: 'test', state: 'Failed' })]}
        edges={[]}
      />,
    )
    expect(screen.queryByRole('button', { name: /^(Promote to|Roll back)/ })).toBeNull()
  })

  it('offers no Promote without the graph, since the upstream state is unknown', () => {
    renderProd('Verified', 'NotStarted', { withGraph: false })
    expect(buttonNames()).toEqual([])
  })

  it('uses the current graph state, not the state the node had when clicked', () => {
    // Clicked while Verified, now promoting: no Roll back.
    renderProd('Verified', 'Promoting', { clicked: 'Verified' })
    expect(buttonNames()).toEqual([])
  })

  it('offers neither on a gate', () => {
    const { nodes, edges } = prodGraph('Verified', 'NotStarted')
    render(<NodeDetail node={nodes[1]} onClose={vi.fn()} pipelineName="my-app" nodes={nodes} edges={edges} />)
    expect(screen.queryByRole('button', { name: /^(Promote to|Roll back)/ })).toBeNull()
  })
})

describe('NodeDetail — promote and rollback ask first (C10b-web-08)', () => {
  beforeEach(() => vi.clearAllMocks())

  it.each([
    { action: 'promote', prod: 'NotStarted', opener: /^Promote to prod$/, title: 'Promote my-app to prod?', confirm: 'Promote to prod', done: 'Promotion started: bundle b' },
    { action: 'rollback', prod: 'Verified', opener: /^Roll back prod$/, title: 'Roll back prod?', confirm: 'Roll back prod', done: 'Rollback started: bundle b' },
  ] as const)('$action: opens a dialog, calls the API only on confirm, then refreshes', async ({ action, prod, opener, title, confirm, done }) => {
    const { api } = await import('../api/client')
    const { onActionDone, rerender, nodes, edges } = renderProd('Verified', prod)
    fireEvent.click(screen.getByRole('button', { name: opener }))
    const dialog = screen.getByRole('dialog', { name: title })
    expect(api[action]).not.toHaveBeenCalled()
    fireEvent.click(within(dialog).getByRole('button', { name: confirm }))
    expect(api[action]).toHaveBeenCalledWith('my-app', 'prod', 'team-a')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(screen.getByRole('status')).toHaveTextContent(done)
    expect(onActionDone).toHaveBeenCalledOnce()

    // The refresh moves the step on and the button goes; the result stays.
    const moved = nodes.map(n => (n.id === 'step-prod' ? { ...n, state: 'Promoting' } : n))
    rerender(
      <NodeDetail
        node={nodes[2]}
        onClose={vi.fn()}
        pipelineName="my-app"
        namespace="team-a"
        onActionDone={onActionDone}
        nodes={moved}
        edges={edges}
      />,
    )
    expect(screen.queryByRole('button', { name: opener })).toBeNull()
    expect(screen.getByRole('status')).toHaveTextContent(done)
  })

  it('Cancel does nothing', async () => {
    const { api } = await import('../api/client')
    renderProd('Verified', 'NotStarted')
    fireEvent.click(screen.getByRole('button', { name: /^Promote to prod$/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(api.promote).not.toHaveBeenCalled()
  })

  it('keeps the dialog open and says what failed', async () => {
    const { api } = await import('../api/client')
    ;(api.promote as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new Error('API error 404: pipeline not found'))
    const { onActionDone } = renderProd('Verified', 'NotStarted')
    fireEvent.click(screen.getByRole('button', { name: /^Promote to prod$/ }))
    const dialog = screen.getByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Promote to prod' }))
    await waitFor(() =>
      expect(within(dialog).getByRole('alert')).toHaveTextContent('Could not promote my-app to prod: API error 404: pipeline not found'),
    )
    expect(onActionDone).not.toHaveBeenCalled()
  })
})

describe('NodeDetail — CEL validation follows the expression (C10b-web-33)', () => {
  beforeEach(() => vi.clearAllMocks())

  it('a slow result for the previous gate does not land on the next one', async () => {
    const { api } = await import('../api/client')
    let resolveFirst: (v: { valid: boolean; error?: string }) => void = () => {}
    ;(api.validateCEL as ReturnType<typeof vi.fn>)
      .mockReturnValueOnce(new Promise(r => { resolveFirst = r }))
      .mockResolvedValueOnce({ valid: true })
    const { rerender } = render(
      <NodeDetail node={makePolicyGateNode({ id: 'gate-a', expression: 'bad(' })} onClose={vi.fn()} />,
    )
    rerender(<NodeDetail node={makePolicyGateNode({ id: 'gate-b', expression: 'true' })} onClose={vi.fn()} />)
    await screen.findByText('✓ valid')
    resolveFirst({ valid: false, error: 'syntax error at 4' })
    await new Promise(r => setTimeout(r, 0))
    expect(screen.queryByText('✗ error')).toBeNull()
    expect(screen.getByText('✓ valid')).toBeInTheDocument()
  })

  it('re-validates when the expression of the same gate changes', async () => {
    const { api } = await import('../api/client')
    const { rerender } = render(
      <NodeDetail node={makePolicyGateNode({ expression: 'true' })} onClose={vi.fn()} />,
    )
    rerender(<NodeDetail node={makePolicyGateNode({ expression: 'false' })} onClose={vi.fn()} />)
    await waitFor(() => expect(api.validateCEL).toHaveBeenCalledTimes(2))
    expect(api.validateCEL).toHaveBeenLastCalledWith('false')
  })
})

describe('NodeDetail — events refresh with the poll (C10b-web-33)', () => {
  beforeEach(() => vi.clearAllMocks())

  it('fetches events again when the parent delivers new step data', async () => {
    const { api } = await import('../api/client')
    const node = makePromotionStepNode()
    const { rerender } = render(<NodeDetail node={node} onClose={vi.fn()} steps={[makeStep()]} />)
    await waitFor(() => expect(api.getStepEvents).toHaveBeenCalledTimes(1))
    rerender(<NodeDetail node={node} onClose={vi.fn()} steps={[makeStep()]} />)
    await waitFor(() => expect(api.getStepEvents).toHaveBeenCalledTimes(2))
    expect(api.getStepEvents).toHaveBeenLastCalledWith('default', 'step-test-bundle')
  })
})

describe('NodeDetail — links from cluster data (C10a-web-13)', () => {
  it.each([
    { name: 'javascript: PR URL', node: { prURL: 'javascript:alert(document.domain)//x/pull/1' } },
    { name: 'javascript: PR URL while waiting for merge', node: { state: 'WaitingForMerge', prURL: 'javascript:alert(1)//x/pull/1' } },
    { name: 'javascript: output URL', node: { outputs: { prURL: 'javascript:alert(1)' } } },
  ])('$name is shown as text, not a link', ({ node }) => {
    render(<NodeDetail node={makePromotionStepNode(node)} onClose={vi.fn()} />)
    expect(screen.queryAllByRole('link')).toHaveLength(0)
    expect(screen.getByText(/javascript:alert/)).toBeInTheDocument()
  })

  it('an https output URL is a link', () => {
    render(<NodeDetail node={makePromotionStepNode({ outputs: { prURL: 'https://github.com/o/r/pull/3' } })} onClose={vi.fn()} />)
    expect(screen.getByRole('link', { name: 'https://github.com/o/r/pull/3' })).toHaveAttribute('href', 'https://github.com/o/r/pull/3')
  })
})

describe('NodeDetail — copy works without the Clipboard API (C10a-web-08)', () => {
  const original = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
  afterEach(() => {
    if (original) Object.defineProperty(navigator, 'clipboard', original)
    else delete (navigator as { clipboard?: unknown }).clipboard
  })

  it('falls back to execCommand on a plain-HTTP origin', () => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    const exec = vi.fn(() => true)
    document.execCommand = exec
    render(<NodeDetail node={makePolicyGateNode()} onClose={vi.fn()} />)
    const btn = screen.getAllByRole('button').find(b => /copy/i.test(b.getAttribute('title') ?? ''))!
    fireEvent.click(btn)
    expect(exec).toHaveBeenCalledWith('copy')
    expect(btn).toHaveAttribute('title', 'Copied!')
  })
})
