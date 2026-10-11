// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, within, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

const api = vi.hoisted(() => ({
  recordApproval: vi.fn(), overrideGate: vi.fn(), rollback: vi.fn(), releaseHold: vi.fn(), pause: vi.fn(),
  resume: vi.fn(), rejectBundle: vi.fn(), getStepEvents: vi.fn(),
}))
vi.mock('../api/client', () => ({ api }))

import { StageDrawer } from './StageDrawer'
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
  pipeline: 'web', bundle: 'web-3', environment: 'prod', state: 'Block', holding: true, reason: '1 of 2 approvals',
  lastEvaluatedAt: '2026-10-11T09:00:00Z', approval: { required: 2, approved: 1, decisions: [{ user: 'bob', decision: 'approve', counted: true }] } }
const freeze: PolicyGate = { name: 'no-weekend--web-3-prod', namespace: 'team', expression: '!schedule.isWeekend', ready: false,
  pipeline: 'web', bundle: 'web-3', environment: 'prod', state: 'Block', holding: true, reason: 'weekend deploys are frozen',
  lastEvaluatedAt: '2026-10-11T09:01:00Z' }
const step: PromotionStep = { name: 'web-web-3-prod', namespace: 'team', pipeline: 'web', bundle: 'web-3', environment: 'prod',
  stepType: 'auto', state: 'Pending', conditions: [{ type: 'Ready', status: 'False', reason: 'Held' }] }

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
  })

  it('puts why it is waiting first: the gate, its message and its age, before the actions', () => {
    renderDrawer([approvalGate, freeze])
    const drawer = screen.getByTestId('stage-drawer')
    const sections = [...drawer.querySelectorAll('[data-section]')].map(s => s.getAttribute('data-section'))
    expect(sections).toEqual(['why', 'actions', 'evidence', 'history'])
    const why = within(drawer.querySelector('[data-section="why"]') as HTMLElement)
    expect(why.getByRole('heading', { name: 'Why it’s waiting' })).toBeInTheDocument()
    expect(why.getByText('Needs 1 more approval (1 of 2) in two-approvers--web-3-prod.')).toBeInTheDocument()
    expect(why.getByText('Held by gate no-weekend--web-3-prod.')).toBeInTheDocument()
    expect(why.getByText('weekend deploys are frozen')).toBeInTheDocument()
    expect(why.getByText('evaluated 2m 0s ago')).toBeInTheDocument()
    expect(screen.getByText('Held by gate two-approvers--web-3-prod')).toBeInTheDocument()
    expect(screen.getByText('1.4.0')).toBeInTheDocument()
    expect(screen.getByText('1.5.0')).toBeInTheDocument()
  })

  it('keeps raw CEL and conditions behind Details', () => {
    renderDrawer()
    const cel = screen.getByText('bundle.approvals >= 2')
    expect(cel.closest('details')).not.toHaveAttribute('open')
    expect(screen.getByText('Ready').closest('details')).not.toHaveAttribute('open')
  })

  it('approve asks first, then records the decision with the comment', async () => {
    const user = userEvent.setup()
    api.recordApproval.mockResolvedValue({ outcome: 'Recorded', approval: 'a', user: 'alice', message: 'alice approves web-3 for prod' })
    const props = renderDrawer()
    await user.click(action('Approve'))
    const dialog = screen.getByRole('dialog', { name: 'Approve web-3 for prod?' })
    expect(dialog).toHaveTextContent('1 of 2 so far')
    await user.type(within(dialog).getByLabelText('Comment (optional)'), 'canary clean')
    await user.click(within(dialog).getByRole('button', { name: 'Approve' }))
    expect(api.recordApproval).toHaveBeenCalledWith({ bundle: 'web-3', environment: 'prod', namespace: 'team', decision: 'approve',
      comment: 'canary clean' })
    expect(await screen.findByRole('status')).toHaveTextContent('Approved. alice approves web-3 for prod')
    expect(props.onChanged).toHaveBeenCalledOnce()
  })

  it('reject asks first; Cancel sends nothing', async () => {
    const user = userEvent.setup()
    renderDrawer()
    await user.click(action('Reject'))
    const dialog = screen.getByRole('dialog', { name: 'Reject web-3 for prod?' })
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(api.recordApproval).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog', { name: 'Reject web-3 for prod?' })).not.toBeInTheDocument()
  })

  it('Esc closes the action dialog first, then the drawer', async () => {
    const user = userEvent.setup()
    const props = renderDrawer()
    await user.click(action('Approve'))
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog', { name: 'Approve web-3 for prod?' })).not.toBeInTheDocument()
    expect(props.onClose).not.toHaveBeenCalled()
    await user.keyboard('{Escape}')
    expect(props.onClose).toHaveBeenCalledOnce()
  })

  it('override needs a reason and sends the gate, the environment and the duration', async () => {
    const user = userEvent.setup()
    api.overrideGate.mockResolvedValue({ message: 'gate overridden', gates: [freeze.name], expiresAt: '' })
    renderDrawer([freeze])
    await user.click(action('Override gate'))
    const dialog = screen.getByRole('dialog', { name: `Override ${freeze.name} in prod?` })
    const confirm = within(dialog).getByRole('button', { name: 'Override gate' })
    expect(confirm).toBeDisabled()
    await user.type(within(dialog).getByLabelText('Reason'), 'P0 hotfix')
    await user.selectOptions(within(dialog).getByLabelText('For'), '240')
    await user.click(confirm)
    expect(api.overrideGate).toHaveBeenCalledWith({ pipeline: 'web', namespace: 'team', gate: freeze.name, environment: 'prod',
      reason: 'P0 hotfix', expiresInMinutes: 240 })
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
    await user.click(action('Reject version'))
    const dialog = screen.getByRole('dialog', { name: 'Reject 1.5.0?' })
    await user.type(within(dialog).getByLabelText('Reason'), 'CVE-2026-1')
    await user.click(within(dialog).getByRole('button', { name: 'Reject version' }))
    expect(api.rejectBundle).toHaveBeenCalledWith('web-3', 'CVE-2026-1', 'team')
    expect(await screen.findByRole('status')).toHaveTextContent('Rejected. bundle web-3 rejected by alice')
  })

  it('a failed request says what failed and keeps the dialog', async () => {
    const user = userEvent.setup()
    api.pause.mockRejectedValue(new Error('POST /api/v1/ui/pause: 403 forbidden'))
    renderDrawer([])
    await user.click(action('Pause pipeline'))
    const dialog = screen.getByRole('dialog', { name: 'Pause web?' })
    await user.click(within(dialog).getByRole('button', { name: 'Pause pipeline' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Could not pause web: POST /api/v1/ui/pause: 403 forbidden')
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
})
