// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { createRef } from 'react'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

const api = vi.hoisted(() => ({ checkAccess: vi.fn(), recordApproval: vi.fn() }))
vi.mock('../api/client', () => ({ api }))

import { resetAccessForTests } from '../access/access'
import { ApprovalsPage, waited, type ApprovalsHandle } from './ApprovalsPage'
import type { AccessCheck, Pipeline, PolicyGate } from '../types'

const now = Date.parse('2026-10-11T10:00:00Z')
const gate = (name: string, env: string, createdAt: string, over: Partial<NonNullable<PolicyGate['approval']>> = {}): PolicyGate => ({
  name, namespace: 'team', expression: 'true', ready: false, state: 'Block', holding: true, pipeline: 'web', bundle: 'web-3',
  environment: env, gateTemplate: 'release-approval', createdAt,
  approval: { required: 2, approved: 1, allowedGroups: ['release-managers'], decisions: [{ user: 'bob', decision: 'approve', counted: true }], ...over },
})
const gates = [gate('a', 'prod', '2026-10-11T09:48:00Z'), gate('b', 'eu', '2026-10-11T08:00:00Z'), gate('c', 'us', '2026-10-11T09:00:00Z', { allowedGroups: ['dba'] })]
const pipelines: Pipeline[] = [{ name: 'web', namespace: 'team', phase: 'Promoting', environmentCount: 3, activeBundleName: 'web-3', activeBundleVersion: '1.5.0' }]

function answer(user?: { username: string; groups: string[] }, allowed = true) {
  api.checkAccess.mockImplementation(async (_ns: string, checks: AccessCheck[]) => ({ ...(user ? { user } : {}), results: checks.map(() => ({ allowed, grantedBy: 'approver or admin' })) }))
}

describe('ApprovalsPage (U7)', () => {
  beforeEach(() => {
    resetAccessForTests()
    api.checkAccess.mockReset()
    api.recordApproval.mockReset()
    answer({ username: 'alice', groups: ['release-managers'] })
  })

  it('lists what waits on me, oldest first, with the wait, the place, the version, the gate and the quorum', async () => {
    render(<ApprovalsPage gates={gates} pipelines={pipelines} now={now} />)
    expect(await screen.findByRole('heading', { name: '2 waiting on you' })).toBeInTheDocument()
    const rows = within(screen.getByRole('list', { name: 'Waiting on you' })).getAllByRole('listitem')
    expect(rows.map(r => within(r).getByRole('link').textContent)).toEqual(['web › eu', 'web › prod'])
    expect(within(rows[1]).getByRole('link', { name: 'web, prod' })).toBeInTheDocument()
    expect(rows[0]).toHaveTextContent('2h')
    expect(rows[1]).toHaveTextContent('12m')
    expect(rows[1]).toHaveTextContent('1.5.0web-3·release-approval') // VersionLabel: the version, then the Bundle name
    expect(within(rows[1]).getByRole('img', { name: '1 of 2 approvals' })).toBeInTheDocument()
    expect(rows[1]).toHaveTextContent('1 of 2 · bob approved')
    expect(within(rows[1]).getByRole('link')).toHaveAttribute('href', '#/p/team/web/env/prod?section=why')
    expect(screen.getByText('1 waiting on someone else')).toBeInTheDocument()
    expect(screen.getByText('Only members of dba can approve it.')).toBeInTheDocument()
  })

  it('approves through the same dialog as the drawer, and says so', async () => {
    const user = userEvent.setup()
    api.recordApproval.mockResolvedValue({ outcome: 'Recorded', approval: 'x', user: 'alice', message: 'Recorded: alice approves web-3 for prod' })
    const onChanged = vi.fn()
    render(<ApprovalsPage gates={[gates[0]]} pipelines={pipelines} now={now} onChanged={onChanged} />)
    await user.click(await screen.findByRole('button', { name: 'Approve' }))
    const dialog = screen.getByRole('dialog', { name: 'Approve 1.5.0 for prod?' })
    await user.click(within(dialog).getByRole('button', { name: 'Approve' }))
    expect(api.recordApproval).toHaveBeenCalledWith({ bundle: 'web-3', environment: 'prod', namespace: 'team', decision: 'approve', comment: undefined })
    expect(await screen.findByRole('status')).toHaveTextContent('Approved. Recorded: alice approves web-3 for prod')
    expect(onChanged).toHaveBeenCalledOnce()
  })

  it('an approval the caller lacks the role for waits on someone else, with the role (QA #1748)', async () => {
    answer({ username: 'alice', groups: ['release-managers'] }, false)
    render(<ApprovalsPage gates={[gates[0]]} pipelines={pipelines} now={now} />)
    expect(await screen.findByRole('heading', { name: 'Nothing waits on you' })).toBeInTheDocument()
    expect(screen.getByText('1 waiting on someone else')).toBeInTheDocument()
    expect(screen.getByText('Approving needs the approver or admin role.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
  })

  it('without sign-in it lists the approvals, says it cannot record one, and keeps the buttons inert', async () => {
    api.checkAccess.mockImplementation(async (_ns: string, checks: AccessCheck[]) => ({
      results: checks.map(() => ({ allowed: false, reason: 'approving needs sign-in with your Kubernetes identity' })),
    }))
    render(<ApprovalsPage gates={[gates[0]]} pipelines={pipelines} now={now} />)
    expect(await screen.findByText(/The UI does not know who you are/)).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '1 waiting for approval' })).toBeInTheDocument()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Approve' })).toHaveAttribute('aria-disabled', 'true'))
    expect(screen.getByRole('button', { name: 'Decline' })).toHaveAttribute('aria-disabled', 'true')
    await userEvent.setup().click(screen.getByRole('button', { name: 'Approve' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('a gate that does not say since when it waits goes last', async () => {
    const undated = { ...gate('d', 'dr', ''), createdAt: undefined, lastEvaluatedAt: undefined }
    render(<ApprovalsPage gates={[undated, gates[0]]} pipelines={pipelines} now={now} />)
    await screen.findByRole('heading', { name: '2 waiting on you' })
    const rows = within(screen.getByRole('list', { name: 'Waiting on you' })).getAllByRole('listitem')
    expect(rows.map(r => within(r).getByRole('link').textContent)).toEqual(['web › prod', 'web › dr'])
  })

  it('j and k move between rows; a approves the row in focus', async () => {
    const ref = createRef<ApprovalsHandle>()
    render(<ApprovalsPage ref={ref} gates={gates} pipelines={pipelines} now={now} />)
    await screen.findByRole('heading', { name: '2 waiting on you' })
    await waitFor(() => expect(api.checkAccess).toHaveBeenCalled())
    act(() => ref.current!.move(1))
    expect(screen.getByRole('link', { name: /web.*eu/ })).toHaveFocus()
    act(() => ref.current!.move(1))
    expect(screen.getByRole('link', { name: /web.*prod/ })).toHaveFocus()
    act(() => { expect(ref.current!.approve()).toBe(true) })
    expect(screen.getByRole('dialog', { name: 'Approve 1.5.0 for prod?' })).toBeInTheDocument()
  })

  it('a with several waiting and none in focus asks to select one; without sign-in it says why, never "nothing waits"', async () => {
    const ref = createRef<ApprovalsHandle>()
    const { unmount } = render(<ApprovalsPage ref={ref} gates={gates} pipelines={pipelines} now={now} />)
    await screen.findByRole('heading', { name: '2 waiting on you' })
    act(() => { expect(ref.current!.approve()).toBe('2 wait on you: select one first (j / k).') })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    unmount()
    resetAccessForTests()
    api.checkAccess.mockImplementation(async (_ns: string, checks: AccessCheck[]) => ({ results: checks.map(() => ({ allowed: false })) }))
    render(<ApprovalsPage ref={ref} gates={[gates[0]]} pipelines={pipelines} now={now} />)
    await screen.findByRole('heading', { name: '1 waiting for approval' })
    act(() => { expect(ref.current!.approve()).toBe('Approving needs sign-in with your Kubernetes identity — run kardinal approve web prod -n team') })
  })

  it('says nothing waits, and how an approval arrives', () => {
    render(<ApprovalsPage gates={[]} pipelines={pipelines} now={now} />)
    expect(screen.getByRole('heading', { name: 'Nothing waits on you' })).toBeInTheDocument()
    expect(screen.getByText(/appears here as soon as a gate asks for it/)).toBeInTheDocument()
  })

  it('formats the wait', () => {
    expect(waited('2026-10-11T09:59:20Z', now)).toBe('40s')
    expect(waited('2026-10-09T06:00:00Z', now)).toBe('2d 4h')
    expect(waited(undefined, now)).toBe('—')
  })
})
