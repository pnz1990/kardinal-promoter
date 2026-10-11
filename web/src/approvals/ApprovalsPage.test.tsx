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
    expect(rows[0]).toHaveTextContent('2h 0m')
    expect(rows[1]).toHaveTextContent('12m')
    expect(rows[1]).toHaveTextContent('1.5.0·release-approval')
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

  it('disables Approve and Decline with the reason when the role is missing, or nobody is signed in', async () => {
    answer({ username: 'alice', groups: ['release-managers'] }, false)
    const { unmount } = render(<ApprovalsPage gates={[gates[0]]} pipelines={pipelines} now={now} />)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Approve' })).toHaveAttribute('aria-disabled', 'true'))
    expect(screen.getByRole('button', { name: 'Approve' })).toHaveAccessibleDescription('Approving needs the approver or admin role.')
    await userEvent.setup().click(screen.getByRole('button', { name: 'Approve' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    unmount()

    resetAccessForTests()
    answer(undefined)
    render(<ApprovalsPage gates={[gates[0]]} pipelines={pipelines} now={now} />)
    expect(await screen.findByText(/The UI does not know who you are/)).toBeInTheDocument()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Decline' })).toHaveAttribute('aria-disabled', 'true'))
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
