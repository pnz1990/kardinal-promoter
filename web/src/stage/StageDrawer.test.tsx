// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, within, act, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

const api = vi.hoisted(() => ({
  recordApproval: vi.fn(), overrideGate: vi.fn(), rollback: vi.fn(), releaseHold: vi.fn(), pause: vi.fn(),
  resume: vi.fn(), rejectBundle: vi.fn(), getStepEvents: vi.fn(), validateCEL: vi.fn(),
}))
vi.mock('../api/client', () => ({ api }))

import { StageDrawer, cliCommand } from './StageDrawer'
import type { Bundle, Pipeline, PolicyGate, PromotionStep } from '../types'

const now = Date.parse('2026-10-11T09:03:00Z')
const pipeline: Pipeline = {
  name: 'web', namespace: 'team', phase: 'Promoting', environmentCount: 3, activeBundleName: 'web-3',
  environmentTopology: [{ name: 'test' }, { name: 'uat', upstreams: ['test'] }, { name: 'prod', upstreams: ['uat'] }],
  environmentStates: { test: 'Verified', uat: 'Verified', prod: 'Pending' },
  deployed: { prod: { bundle: 'web-2', version: '1.4.0', verifiedAt: '2026-10-10T09:03:00Z' } },
}
const b = (name: string, tag: string, created: string, prod?: string): Bundle => ({ name, namespace: 'team', phase: 'Verified',
  type: 'image', pipeline: 'web', createdAt: created, images: [{ repository: 'r', tag }],
  environments: prod ? [{ name: 'prod', phase: prod }] : [] })
const bundles = [b('web-1', '1.3.0', '2026-10-01T00:00:00Z', 'Verified'), b('web-2', '1.4.0', '2026-10-05T00:00:00Z', 'Verified'),
  { ...b('web-3', '1.5.0', '2026-10-09T00:00:00Z'), phase: 'Promoting' }]
const approvalGate: PolicyGate = { name: 'two-approvers--web-3-prod', namespace: 'team', expression: 'bundle.approvals >= 2', ready: false,
  pipeline: 'web', bundle: 'web-3', environment: 'prod', gateTemplate: 'two-approvers', maxOverrideMinutes: 1440,
  state: 'Block', holding: true, reason: '1 of 2 approvals',
  lastEvaluatedAt: '2026-10-11T09:00:00Z', approval: { required: 2, approved: 1, decisions: [{ user: 'bob', decision: 'approve', counted: true }] } }
const freeze: PolicyGate = { name: 'no-weekend--web-3-prod', namespace: 'team', expression: '!schedule.isWeekend', ready: false,
  pipeline: 'web', bundle: 'web-3', environment: 'prod', gateTemplate: 'no-weekend', maxOverrideMinutes: 1440,
  state: 'Block', holding: true, reason: 'weekend deploys are frozen',
  lastEvaluatedAt: '2026-10-11T09:01:00Z' }
const step: PromotionStep = { name: 'web-web-3-prod', namespace: 'team', pipeline: 'web', bundle: 'web-3', environment: 'prod',
  stepType: 'auto', state: 'Pending', conditions: [{ type: 'Ready', status: 'False', reason: 'Held' }] }

/** A button next to a reason in "Why it's waiting". */
const reasonButton = (name: string) => within(document.querySelector('[data-section="why"]') as HTMLElement).getByRole('button', { name })
/** An action button of the Actions section. */
const action = (name: string) => within(document.querySelector('[data-section="actions"]') as HTMLElement).getByRole('button', { name })

function renderDrawer(gates: PolicyGate[] = [approvalGate], over: Partial<Parameters<typeof StageDrawer>[0]> = {}) {
  const props = { namespace: 'team', pipeline: 'web', env: 'prod', onClose: vi.fn(), onNavigate: vi.fn(), onChanged: vi.fn(),
    data: { pipeline, bundles, steps: [step], gates }, now, ...over }
  render(<StageDrawer {...props} />)
  return props
}

describe('StageDrawer (U3)', () => {
  beforeEach(() => {
    Object.values(api).forEach(f => f.mockReset())
    api.getStepEvents.mockResolvedValue([])
    api.validateCEL.mockResolvedValue({ valid: true })
  })

  it('puts why it is waiting first: the gate, its message and its age, before the actions', () => {
    renderDrawer([approvalGate, freeze])
    const drawer = screen.getByTestId('stage-drawer')
    const sections = [...drawer.querySelectorAll('[data-section]')].map(s => s.getAttribute('data-section'))
    expect(sections).toEqual(['why', 'actions', 'evidence', 'history'])
    const why = within(drawer.querySelector('[data-section="why"]') as HTMLElement)
    expect(why.getByRole('heading', { name: 'Why it’s waiting' })).toBeInTheDocument()
    // Gates read as the user named them; the generated instance is under Kubernetes details.
    expect(why.getByText('Needs 1 more approval (1 of 2) in two-approvers.')).toBeInTheDocument()
    expect(why.getByText('Held by gate no-weekend.')).toBeInTheDocument()
    expect(why.getByText('weekend deploys are frozen')).toBeInTheDocument()
    expect(why.getByText('evaluated 2m 0s ago')).toBeInTheDocument()
    const gateK8s = [...why.getByText('Needs 1 more approval (1 of 2) in two-approvers.').closest('li')!.querySelectorAll('details.k-k8s')]
    expect(gateK8s).toHaveLength(1)
    expect(gateK8s[0]).not.toHaveAttribute('open')
    expect(gateK8s[0]).toHaveTextContent('KindPolicyGateNametwo-approvers--web-3-prodNamespaceteam')
    // A CEL gate blocks, so the environment reads as held by it.
    expect(screen.getByText('Held by gate no-weekend')).toBeInTheDocument()
    expect(screen.getByText('1.4.0')).toBeInTheDocument()
    expect(screen.getByText('1.5.0')).toBeInTheDocument()
  })

  it('an approval wait shows the CLI command that approves, with -n outside default, and a copy button', () => {
    renderDrawer([approvalGate, freeze])
    const why = within(screen.getByTestId('stage-drawer').querySelector('[data-section="why"]') as HTMLElement)
    expect(why.getByText('kardinal approve web prod -n team')).toBeInTheDocument()
    expect(why.getByRole('button', { name: 'Copy kardinal approve web prod -n team' })).toBeInTheDocument()
    // Only the approval: a CEL gate is not something approve moves on.
    expect(why.getAllByText(/^kardinal /)).toHaveLength(1)
  })

  it('the CLI command has no -n in default', () => {
    expect(cliCommand({ kind: 'approval', title: '' }, 'demo', 'prod', 'default')).toBe('kardinal approve demo prod')
    expect(cliCommand({ kind: 'approval', title: '' }, 'demo', 'prod', 'team')).toBe('kardinal approve demo prod -n team')
    expect(cliCommand({ kind: 'gate', title: '' }, 'demo', 'prod', 'default')).toBeUndefined()
  })

  it('an approval gate collecting its quorum reads as waiting, not blocked', () => {
    renderDrawer([approvalGate])
    const chip = screen.getByText('Waiting for approval in two-approvers')
    expect(chip.closest('.stage-chip')).toHaveClass('stage-chip--waiting')
    const evidence = within(document.querySelector('[data-section="evidence"]') as HTMLElement)
    expect(evidence.getByText('Waiting for approval')).toBeInTheDocument()
  })

  it('shows the version once: the Bundle name only when it says something more', () => {
    const same: Bundle = { ...b('web-3', 'web-3', '2026-10-09T00:00:00Z'), phase: 'Promoting', version: 'web-3' }
    renderDrawer([], { data: { pipeline, bundles: [same], steps: [], gates: [] } })
    expect(screen.getAllByText('web-3')).toHaveLength(1)
  })

  it('offers Approve once, next to its reason; one primary; pipeline-wide actions apart and quiet', () => {
    renderDrawer([approvalGate])
    expect(screen.getAllByRole('button', { name: 'Approve' })).toHaveLength(1)
    expect(reasonButton('Approve')).toHaveClass('stage-button--primary')
    expect(document.querySelectorAll('.stage-button--primary')).toHaveLength(1)
    expect(action('Decline')).toHaveClass('stage-button--destructive')
    const pipelineWide = document.querySelector('.stage-pipeline-actions') as HTMLElement
    expect(within(pipelineWide).getByText('Whole pipeline')).toBeInTheDocument()
    for (const name of ['Pause pipeline', 'Reject version']) {
      expect(within(pipelineWide).getByRole('button', { name })).toHaveClass('stage-button--quiet')
    }
  })

  it('says what evidence and history will show before the first promotion', () => {
    renderDrawer([], { data: { pipeline, bundles, steps: [], gates: [] } })
    expect(screen.getByText(/Evidence appears when this environment gets its first promotion/)).toBeInTheDocument()
    expect(screen.getByText('Events appear here once a promotion starts in this environment.')).toBeInTheDocument()
  })

  it('keeps raw CEL and conditions behind Details', () => {
    renderDrawer()
    const cel = document.querySelector('.cel-block') as HTMLElement
    expect(cel).toHaveTextContent('bundle.approvals >= 2')
    expect(cel.closest('details')).not.toHaveAttribute('open')
    expect(screen.getByText('Ready').closest('details')).not.toHaveAttribute('open')
  })

  it('approve asks first, then records the decision with the comment', async () => {
    const user = userEvent.setup()
    api.recordApproval.mockResolvedValue({ outcome: 'Recorded', approval: 'a', user: 'alice', message: 'alice approves web-3 for prod' })
    const props = renderDrawer()
    await user.click(reasonButton('Approve'))
    const dialog = screen.getByRole('dialog', { name: 'Approve 1.5.0 for prod?' })
    expect(dialog).toHaveTextContent('1 of 2 so far')
    await user.type(within(dialog).getByLabelText('Comment (optional)'), 'canary clean')
    await user.click(within(dialog).getByRole('button', { name: 'Approve' }))
    expect(api.recordApproval).toHaveBeenCalledWith({ bundle: 'web-3', environment: 'prod', namespace: 'team', decision: 'approve',
      comment: 'canary clean' })
    expect(await screen.findByRole('status')).toHaveTextContent('Approved. alice approves web-3 for prod')
    expect(props.onChanged).toHaveBeenCalledOnce()
  })

  it('decline asks first; Cancel sends nothing; Decline records a reject decision', async () => {
    const user = userEvent.setup()
    api.recordApproval.mockResolvedValue({ outcome: 'Recorded', approval: 'a', user: 'alice', message: 'alice rejects web-3 for prod' })
    renderDrawer()
    await user.click(action('Decline'))
    let dialog = screen.getByRole('dialog', { name: 'Decline 1.5.0 for prod?' })
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(api.recordApproval).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog', { name: 'Decline 1.5.0 for prod?' })).not.toBeInTheDocument()
    await user.click(action('Decline'))
    dialog = screen.getByRole('dialog', { name: 'Decline 1.5.0 for prod?' })
    await user.click(within(dialog).getByRole('button', { name: 'Decline' }))
    expect(api.recordApproval).toHaveBeenCalledWith(expect.objectContaining({ decision: 'reject' }))
    expect(await screen.findByRole('status')).toHaveTextContent(/^Declined\./)
  })

  it('Esc closes the action dialog first, then the drawer', async () => {
    const user = userEvent.setup()
    const props = renderDrawer()
    await user.click(reasonButton('Approve'))
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog', { name: 'Approve 1.5.0 for prod?' })).not.toBeInTheDocument()
    expect(props.onClose).not.toHaveBeenCalled()
    await user.keyboard('{Escape}')
    expect(props.onClose).toHaveBeenCalledOnce()
  })

  it('override needs a reason and sends the gate, the environment and the duration', async () => {
    const user = userEvent.setup()
    api.overrideGate.mockResolvedValue({ message: 'gate overridden', gates: [freeze.name], expiresAt: '' })
    renderDrawer([freeze])
    await user.click(reasonButton('Override gate'))
    const dialog = screen.getByRole('dialog', { name: 'Override no-weekend in prod?' })
    const confirm = within(dialog).getByRole('button', { name: 'Override gate' })
    expect(confirm).toBeDisabled()
    await user.type(within(dialog).getByLabelText('Reason'), 'P0 hotfix')
    await user.selectOptions(within(dialog).getByLabelText('For'), '240')
    await user.click(confirm)
    expect(api.overrideGate).toHaveBeenCalledWith({ pipeline: 'web', namespace: 'team', gate: freeze.name, environment: 'prod',
      reason: 'P0 hotfix', expiresInMinutes: 240 })
  })

  it('override opens on the gate whose reason was clicked, with the controller\'s cap', async () => {
    const user = userEvent.setup()
    const hours: PolicyGate = { ...freeze, name: 'business-hours--web-3-prod', gateTemplate: 'business-hours', reason: 'outside 9-17',
      maxOverrideMinutes: 240 }
    renderDrawer([freeze, hours])
    const items = document.querySelectorAll('.stage-why__item')
    await user.click(within(items[1] as HTMLElement).getByRole('button', { name: 'Override gate' }))
    const dialog = screen.getByRole('dialog', { name: 'Override business-hours in prod?' })
    expect(within(dialog).getByLabelText('Gate')).toHaveValue(hours.name)
    await user.selectOptions(within(dialog).getByLabelText('Gate'), hours.name)
    const options = [...within(dialog).getByLabelText('For').querySelectorAll('option')].map(o => o.value)
    expect(options).toEqual(['15', '60', '240'])
  })

  it('roll back sends the chosen version (toBundle), and the hold when asked', async () => {
    const user = userEvent.setup()
    api.rollback.mockResolvedValue({ bundle: 'web-rollback-x', message: 'rollback started' })
    renderDrawer([])
    await user.click(action('Roll back'))
    const dialog = screen.getByRole('dialog', { name: 'Roll prod back to 1.3.0?' })
    await user.click(within(dialog).getByLabelText('Hold prod on this version until released'))
    await user.type(within(dialog).getByLabelText('Why hold'), 'sessions break')
    await user.click(within(dialog).getByRole('button', { name: 'Roll back' }))
    expect(api.rollback).toHaveBeenCalledWith('web', 'prod', 'team', 'web-1', 'sessions break')
  })

  it('reject version needs a reason and calls the reject endpoint', async () => {
    const user = userEvent.setup()
    api.rejectBundle.mockResolvedValue({ message: 'bundle web-3 rejected by alice' })
    renderDrawer([])
    await user.click(screen.getByRole('button', { name: 'Reject version' }))
    const dialog = screen.getByRole('dialog', { name: 'Reject 1.5.0?' })
    await user.type(within(dialog).getByLabelText('Reason'), 'CVE-2026-1')
    await user.click(within(dialog).getByRole('button', { name: 'Reject version' }))
    expect(api.rejectBundle).toHaveBeenCalledWith('web-3', 'CVE-2026-1', 'team')
    expect(await screen.findByRole('status')).toHaveTextContent('Rejected. bundle web-3 rejected by alice')
  })

  it('a failed request says what failed and keeps the dialog', async () => {
    const user = userEvent.setup()
    api.pause.mockRejectedValue(new Error('POST /api/v1/ui/pause: timed out'))
    renderDrawer([])
    await user.click(screen.getByRole('button', { name: 'Pause pipeline' }))
    const dialog = screen.getByRole('dialog', { name: 'Pause web?' })
    await user.click(within(dialog).getByRole('button', { name: 'Pause pipeline' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Could not pause web: POST /api/v1/ui/pause: timed out')
  })

  it('a 403 says which role grants the action; a 409 shows the server\'s text and how to retry', async () => {
    const user = userEvent.setup()
    api.pause.mockRejectedValue(new Error('API error 403: forbidden: user "bob" cannot update pipelines/pause.kardinal.io "web" in namespace team; the chart\'s promoter or admin role grants it'))
    renderDrawer([])
    await user.click(screen.getByRole('button', { name: 'Pause pipeline' }))
    let dialog = screen.getByRole('dialog', { name: 'Pause web?' })
    await user.click(within(dialog).getByRole('button', { name: 'Pause pipeline' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('You are not allowed to pause web: forbidden: user "bob" cannot update pipelines/pause.kardinal.io "web" in namespace team; the chart\'s promoter or admin role grants it')
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    api.rejectBundle.mockRejectedValue(new Error('API error 409: bundle web-3 is already rejected'))
    await user.click(screen.getByRole('button', { name: 'Reject version' }))
    dialog = screen.getByRole('dialog', { name: 'Reject 1.5.0?' })
    await user.type(within(dialog).getByLabelText('Reason'), 'CVE')
    await user.click(within(dialog).getByRole('button', { name: 'Reject version' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Could not reject web-3: bundle web-3 is already rejected. Refresh to see the current state, then try again.')
  })

  it('a fleet lists its targets, and a target opens through onNavigate', async () => {
    const user = userEvent.setup()
    const fleet: Pipeline = { ...pipeline, environmentTopology: [{ name: 'prod', fleet: { targets: ['prod-eu', 'prod-us'], maxConcurrent: 1 } }],
      environmentStates: { 'prod-eu': 'Verified' } }
    const props = renderDrawer([], { data: { pipeline: fleet, bundles, steps: [], gates: [] } })
    expect(screen.getByText('prod promotes to 2 targets, 1 at a time.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'prod-eu' }))
    expect(props.onNavigate).toHaveBeenCalledWith('prod-eu', 'why')
  })

  it('the close button calls onClose', async () => {
    const user = userEvent.setup()
    const props = renderDrawer()
    await user.click(screen.getByRole('button', { name: 'Close prod' }))
    expect(props.onClose).toHaveBeenCalledOnce()
    await act(async () => {})
  })

  it('Details checks the gate\'s CEL with the controller; the step shows its sub-steps, running time and events (UI-NODE-01)', async () => {
    api.validateCEL.mockResolvedValue({ valid: false, error: 'undeclared reference to \'bundel\'' })
    api.getStepEvents.mockResolvedValue([{ type: 'Normal', reason: 'WaitingForMerge', message: 'PR opened', count: 1, lastTimestamp: '2026-10-11T09:00:00Z' }])
    const running: PromotionStep = { ...step, state: 'WaitingForMerge', steps: [
      { name: 'git-clone', state: 'Completed', startedAt: '2026-10-11T09:00:00Z', completedAt: '2026-10-11T09:00:02Z', durationMs: 2000 },
      { name: 'open-pr', state: 'InProgress', startedAt: '2026-10-11T09:00:02Z' }] }
    renderDrawer([freeze], { data: { pipeline, bundles, steps: [running], gates: [freeze] } })
    expect(api.validateCEL).not.toHaveBeenCalled()
    const details = document.querySelector('[data-section="why"] details') as HTMLDetailsElement
    act(() => { details.open = true; details.dispatchEvent(new Event('toggle')) })
    await waitFor(() => expect(document.querySelector('[data-cel-check="error"]')).not.toBeNull())
    const chip = document.querySelector('[data-cel-check="error"]') as HTMLElement
    expect(chip).toHaveTextContent('error')
    expect(chip).toHaveAttribute('title', "CEL compile error: undeclared reference to 'bundel'")
    const items = within(screen.getByRole('list', { name: 'Promotion steps' })).getAllByRole('listitem')
    expect(items.map(i => i.getAttribute('data-step-state'))).toEqual(['Completed', 'InProgress'])
    expect(items[1]).toHaveTextContent('waiting for merge')
    expect(screen.getByTestId('stage-elapsed')).toHaveTextContent('Running for 3m 0s')
    expect(await screen.findByTestId('events-panel')).toHaveTextContent('PR opened')
  })
})
