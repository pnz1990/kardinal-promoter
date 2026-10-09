// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { ApprovalQuorum, approverRule, quorumSummary } from './ApprovalQuorum'
import type { GateApproval } from '../types'

const now = Date.parse('2026-10-09T12:00:00Z')

const waiting: GateApproval = {
  required: 2, allowedGroups: ['release-managers'], excludeAuthor: true, approved: 1,
  decisions: [
    { user: 'alice', decision: 'approve', counted: true, comment: 'looks good', firstSeenAt: '2026-10-09T09:00:00Z' },
    { user: 'mallory', decision: 'approve', counted: false, reason: 'not in allowedUsers or allowedGroups' },
  ],
}

describe('ApprovalQuorum', () => {
  it('says how far the quorum is, and who may approve', () => {
    expect(quorumSummary(waiting)).toBe('1 of 2 approvals')
    expect(quorumSummary({ required: 1, approved: 0 })).toBe('0 of 1 approval')
    expect(quorumSummary({ required: 2, approved: 2 })).toBe('Approved (2 of 2)')
    expect(quorumSummary({ required: 2, approved: 2, rejected: true, decisions: [{ user: 'bob', decision: 'reject', counted: true }] })).toBe('Rejected by bob')
    expect(approverRule(waiting)).toBe("From members of release-managers; not the Bundle's creator")
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
    const { rerender } = render(<ApprovalQuorum approval={waiting} bundle="app-1" environment="prod" now={now} />)
    expect(screen.getByText('kardinal approve app-1 --env prod')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Copy the approve command/ })).toBeInTheDocument()
    rerender(<ApprovalQuorum approval={{ ...waiting, approved: 2 }} bundle="app-1" environment="prod" now={now} />)
    expect(screen.queryByText(/kardinal approve/)).not.toBeInTheDocument()
    expect(screen.getByRole('meter')).toHaveAttribute('aria-valuetext', 'Approved (2 of 2)')
  })

  it('a large quorum is text only, and no decisions says so', () => {
    const { container } = render(<ApprovalQuorum approval={{ required: 25, approved: 3 }} now={now} />)
    expect(container.querySelectorAll('.approval-quorum__pip')).toHaveLength(0)
    expect(screen.getByText('3 of 25 approvals')).toBeInTheDocument()
    expect(screen.getByText('No decisions yet.')).toBeInTheDocument()
  })
})
