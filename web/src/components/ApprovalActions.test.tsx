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
