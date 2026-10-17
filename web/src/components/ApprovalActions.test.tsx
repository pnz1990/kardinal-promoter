// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

const api = vi.hoisted(() => ({ recordApproval: vi.fn() }))
vi.mock('../api/client', () => ({ api }))

import { ApprovalActions } from './ApprovalActions'

describe('ApprovalActions (E6)', () => {
  beforeEach(() => { api.recordApproval.mockReset() })

  it('approves with a comment and reports what the server recorded', async () => {
    const user = userEvent.setup()
    const onDone = vi.fn()
    api.recordApproval.mockResolvedValue({ outcome: 'Recorded', approval: 'a1', user: 'alice', message: 'Recorded: alice approves app-v2 for prod' })
    render(<ApprovalActions bundle="app-v2" environment="prod" namespace="team-a" onDone={onDone} />)
    await user.type(screen.getByLabelText('Comment (optional)'), ' canary is clean ')
    await user.click(screen.getByRole('button', { name: 'Approve' }))
    expect(api.recordApproval).toHaveBeenCalledWith({
      bundle: 'app-v2', environment: 'prod', namespace: 'team-a', decision: 'approve', comment: 'canary is clean',
    })
    expect(await screen.findByRole('status')).toHaveTextContent('Recorded: alice approves app-v2 for prod')
    expect(onDone).toHaveBeenCalledOnce()
  })

  it('asks for the gate\'s approval fields: a missing required one is refused before anything is sent; the values go with the approve', async () => {
    const user = userEvent.setup()
    api.recordApproval.mockResolvedValue({ outcome: 'Recorded', approval: 'a1', user: 'alice', message: 'Recorded' })
    const fields = [
      { name: 'ticketID', type: 'string', required: true, pattern: '^CHG[0-9]{7}$', description: 'ServiceNow change number' },
      { name: 'riskAcknowledged', type: 'bool', required: true, mustBe: 'true', description: 'I have read the migration notes' },
      { name: 'tier', type: 'enum', values: ['low', 'high'] },
    ]
    render(<ApprovalActions bundle="app-v2" environment="prod" namespace="team-a" fields={fields} />)
    await user.click(screen.getByRole('button', { name: 'Approve' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('ticketID is required; riskAcknowledged is required')
    expect(api.recordApproval).not.toHaveBeenCalled()

    await user.type(screen.getByLabelText('ticketID'), 'CHG12')
    await user.click(screen.getByLabelText('I have read the migration notes'))
    await user.click(screen.getByRole('button', { name: 'Approve' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('ticketID must match ^CHG[0-9]{7}$')
    expect(api.recordApproval).not.toHaveBeenCalled()

    await user.clear(screen.getByLabelText('ticketID'))
    await user.type(screen.getByLabelText('ticketID'), 'CHG0031337')
    await user.selectOptions(screen.getByLabelText('tier (optional)'), 'high')
    await user.click(screen.getByRole('button', { name: 'Approve' }))
    expect(api.recordApproval).toHaveBeenCalledWith({
      bundle: 'app-v2', environment: 'prod', namespace: 'team-a', decision: 'approve',
      fields: { ticketID: 'CHG0031337', riskAcknowledged: 'true', tier: 'high' },
    })
  })

  it('a reject needs no fields', async () => {
    const user = userEvent.setup()
    api.recordApproval.mockResolvedValue({ outcome: 'Recorded', approval: 'a1', user: 'alice', message: 'Recorded' })
    render(<ApprovalActions bundle="app-v2" environment="prod" namespace="team-a"
      fields={[{ name: 'ticketID', type: 'string', required: true }]} />)
    await user.click(screen.getByRole('button', { name: 'Reject' }))
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Reject' }))
    expect(api.recordApproval).toHaveBeenCalledWith({ bundle: 'app-v2', environment: 'prod', namespace: 'team-a', decision: 'reject' })
  })

  it('restricted: every button stays in place but inert, its reason the description; nothing is sent', async () => {
    const user = userEvent.setup()
    const why = { approve: 'Approving needs sign-in.', reject: 'Declining needs sign-in.', revoke: 'Revoking needs sign-in.' }
    render(<ApprovalActions bundle="app-v2" environment="prod" namespace="team-a" reasonFor={a => why[a]} />)
    for (const [name, a] of [['Approve', 'approve'], ['Reject', 'reject'], ['Revoke mine', 'revoke']] as const) {
      const b = screen.getByRole('button', { name })
      expect(b).toHaveAttribute('aria-disabled', 'true')
      expect(b).toHaveAccessibleDescription(why[a])
      await user.click(b)
    }
    expect(screen.getByLabelText('Comment (optional)')).toBeDisabled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(api.recordApproval).not.toHaveBeenCalled()
  })

  it('asks before rejecting, and Cancel sends nothing', async () => {
    const user = userEvent.setup()
    api.recordApproval.mockResolvedValue({ outcome: 'Recorded', approval: 'a1', user: 'alice', message: 'Recorded: alice rejects app-v2 for prod' })
    render(<ApprovalActions bundle="app-v2" environment="prod" namespace="team-a" />)
    await user.click(screen.getByRole('button', { name: 'Reject' }))
    const dialog = screen.getByRole('dialog', { name: 'Reject app-v2 for prod?' })
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(api.recordApproval).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Reject' }))
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Reject' }))
    expect(api.recordApproval).toHaveBeenCalledWith({
      bundle: 'app-v2', environment: 'prod', namespace: 'team-a', decision: 'reject', comment: undefined,
    })
    expect(await screen.findByRole('status')).toHaveTextContent('rejects')
  })

  it('revokes, and says why when the server refuses', async () => {
    const user = userEvent.setup()
    api.recordApproval.mockRejectedValue(new Error('API error 403: an approval names who decided: it needs the UI\'s TokenReview mode'))
    render(<ApprovalActions bundle="app-v2" environment="prod" namespace="team-a" />)
    await user.click(screen.getByRole('button', { name: 'Revoke mine' }))
    expect(api.recordApproval).toHaveBeenCalledWith({ bundle: 'app-v2', environment: 'prod', namespace: 'team-a', revoke: true })
    expect(await screen.findByRole('alert')).toHaveTextContent('TokenReview')
  })
})
