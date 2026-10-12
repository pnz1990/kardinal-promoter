// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'

const api = vi.hoisted(() => ({ checkAccess: vi.fn(), recordApproval: vi.fn() }))
vi.mock('../api/client', () => ({ api }))

import { resetAccessForTests } from '../access/access'
import { ApprovalQuorum, approverRule, quorumSummary } from './ApprovalQuorum'
import type { AccessCheck, GateApproval } from '../types'

const now = Date.parse('2026-10-09T12:00:00Z')

const waiting: GateApproval = {
  required: 2, allowedGroups: ['release-managers'], excludeAuthor: true, approved: 1,
  decisions: [
    { user: 'alice', decision: 'approve', counted: true, comment: 'looks good', firstSeenAt: '2026-10-09T09:00:00Z' },
    { user: 'mallory', decision: 'approve', counted: false, reason: 'not in allowedUsers or allowedGroups' },
  ],
}

describe('ApprovalQuorum', () => {
  beforeEach(() => {
    resetAccessForTests()
    api.checkAccess.mockReset()
    api.checkAccess.mockImplementation(async (_ns: string, checks: AccessCheck[]) => ({
      user: { username: 'alice', groups: ['release-managers'] }, results: checks.map(() => ({ allowed: true })) }))
  })
  it('says how far the quorum is, and who may approve', () => {
    expect(quorumSummary(waiting)).toBe('1 of 2 approvals')
    expect(quorumSummary({ required: 1, approved: 0 })).toBe('0 of 1 approval')
    expect(quorumSummary({ required: 2, approved: 2 })).toBe('Approved (2 of 2)')
    expect(quorumSummary({ required: 2, approved: 2, rejected: true, decisions: [{ user: 'bob', decision: 'reject', counted: true }] })).toBe('Rejected by bob')
    expect(approverRule(waiting)).toBe("From members of release-managers; not the person who shipped the version")
    expect(approverRule({ required: 1, approved: 0, allowedUsers: ['alice', 'bob'] })).toBe('From alice, bob')
    expect(approverRule({ required: 1, approved: 0 })).toBe('From anyone allowed to create Approvals')
  })

  it('draws one pip per approval needed, filled for each counted approval', () => {
    const { container } = render(<ApprovalQuorum approval={waiting} bundle="app-1" environment="prod" now={now} />)
    const meter = screen.getByRole('meter', { name: 'Approvals' })
    expect(meter).toHaveAttribute('aria-valuenow', '1')
    expect(meter).toHaveAttribute('aria-valuemax', '2')
    expect(meter).toHaveAttribute('aria-valuetext', '1 of 2 approvals')
    expect(container.querySelectorAll('.approval-quorum__pip')).toHaveLength(2)
    expect(container.querySelectorAll('.approval-quorum__pip[data-filled]')).toHaveLength(1)
    expect(container.firstChild).toHaveAttribute('data-state', 'waiting')
  })

  it('lists every decision: counted or not and why, its comment and age', () => {
    render(<ApprovalQuorum approval={waiting} bundle="app-1" environment="prod" now={now} />)
    const rows = within(screen.getByRole('list', { name: 'Approval decisions' })).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent('alice')
    expect(rows[0]).toHaveTextContent('approved')
    expect(rows[0]).toHaveTextContent('3h ago')
    expect(rows[0]).toHaveTextContent('looks good')
    expect(rows[1]).toHaveTextContent('not counted: not in allowedUsers or allowedGroups')
    expect(rows[1]).toHaveAttribute('data-counted', 'false')
  })

  it('shows the CLI command to approve until the quorum is met', () => {
    // The stage drawer's command (C5): kardinal approve <pipeline> <env>, -n outside default.
    const { rerender } = render(<ApprovalQuorum approval={waiting} pipeline="app" bundle="app-1" environment="prod" now={now} />)
    expect(screen.getByText('kardinal approve app prod')).toBeInTheDocument()
    rerender(<ApprovalQuorum approval={waiting} pipeline="app" bundle="app-1" environment="prod" namespace="team-a" now={now} />)
    expect(screen.getByText('kardinal approve app prod -n team-a')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Copy the approve command/ })).toBeInTheDocument()
    rerender(<ApprovalQuorum approval={{ ...waiting, approved: 2 }} pipeline="app" bundle="app-1" environment="prod" now={now} />)
    expect(screen.queryByText(/kardinal approve/)).not.toBeInTheDocument()
    expect(screen.getByRole('meter')).toHaveAttribute('aria-valuetext', 'Approved (2 of 2)')
  })

  it('without sign-in the buttons are inert, with one line naming the command; on an approved gate, the revoke command', async () => {
    api.checkAccess.mockImplementation(async (_ns: string, checks: AccessCheck[]) => ({ results: checks.map(() => ({ allowed: false })) }))
    const { rerender } = render(<ApprovalQuorum approval={waiting} pipeline="app" bundle="app-1" environment="prod" namespace="team-a" now={now} />)
    await waitFor(() => expect(document.querySelector('.sign-in-note')).not.toBeNull())
    expect(document.querySelector('.sign-in-note')).toHaveTextContent('Approving needs sign-in with your Kubernetes identity — run kardinal approve app prod -n team-a')
    expect(screen.queryByText('Or from the CLI:')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reject' })).toHaveAccessibleDescription(
      'Declining needs sign-in with your Kubernetes identity — run kardinal approve app prod -n team-a --decision reject')
    rerender(<ApprovalQuorum approval={{ ...waiting, approved: 2 }} pipeline="app" bundle="app-1" environment="prod" namespace="team-a" now={now} />)
    expect(document.querySelector('.sign-in-note')).toHaveTextContent(
      'Revoking your decision needs sign-in with your Kubernetes identity — run kardinal approve app prod -n team-a --revoke')
    expect(screen.getByRole('button', { name: 'Revoke mine' })).toHaveAttribute('aria-disabled', 'true')
  })

  it('a large quorum is text only, and no decisions says so', () => {
    const { container } = render(<ApprovalQuorum approval={{ required: 25, approved: 3 }} now={now} />)
    expect(container.querySelectorAll('.approval-quorum__pip')).toHaveLength(0)
    expect(screen.getByText('3 of 25 approvals')).toBeInTheDocument()
    expect(screen.getByText('No decisions yet.')).toBeInTheDocument()
  })
})

describe('ApprovalQuorum — a gate of a version that is not on its way in', () => {
  it('names the Bundle in the CLI line, so the command approves that version', () => {
    const waiting = { required: 2, approved: 1, decisions: [] }
    render(<ApprovalQuorum approval={waiting} pipeline="app" bundle="app-1" environment="prod" current={false} />)
    expect(screen.getByText('kardinal approve app-1 --env prod')).toBeInTheDocument()
  })
})
