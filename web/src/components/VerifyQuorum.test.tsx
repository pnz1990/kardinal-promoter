// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'

import { VerifyQuorum } from './VerifyQuorum'
import { inconclusiveWaiting, verdictChip, verifierRule, verifySummary, verifyState } from './verifyModel'
import type { GateVerify } from '../types'

const now = Date.parse('2026-10-14T12:00:00Z')

const waiting: GateVerify = {
  check: 'exploratory-qa', environment: 'uat', required: 2, allowedAgents: ['qa-explorer'], onInconclusive: 'retry',
  template: 'qa-explorer', passed: 1, failed: 0,
  verdicts: [
    { name: 'r1-a1', verifier: 'agent qa-explorer for bob', result: 'pass', counted: true, run: 1, attempt: 1,
      summary: '41 pages, all good', evidenceURL: 'https://ci.example.com/1', findings: 1, createdAt: '2026-10-14T11:00:00Z',
      verifierActor: { kind: 'agent', agent: 'qa-explorer', sponsor: 'bob', text: 'agent qa-explorer for bob' } },
    { name: 'self', verifier: 'agent fixer for alice', result: 'pass', counted: false,
      reason: "not counted: the author's party (agent fixer for alice)" },
  ],
}

describe('VerifyQuorum', () => {
  it('says how far the quorum is, and who may verify', () => {
    expect(verifySummary(waiting)).toBe('1 of 2 verdicts for exploratory-qa')
    expect(verifySummary({ ...waiting, passed: 2 })).toBe('Verified (2 of 2)')
    expect(verifySummary({ ...waiting, failed: 1 })).toBe('Failed (1 failing verdict)')
    expect(verifyState({ ...waiting, failed: 1, maxFailures: 1 })).toBe('waiting')
    expect(verifierRule(waiting)).toBe('exploratory-qa in uat, from agents qa-explorer and the people allowed; runs qa-explorer in the cluster; inconclusive: retry')
    expect(verifierRule({ ...waiting, allowedAgents: [], template: undefined })).toContain('from no agents')
  })
  it('lists every verdict with why it does not count, its run, evidence and the CLI command', () => {
    render(<VerifyQuorum verify={waiting} pipeline="checkout" namespace="payments" now={now} />)
    const meter = screen.getByRole('meter', { name: 'Verdicts' })
    expect(meter).toHaveAttribute('aria-valuenow', '1')
    const list = screen.getByRole('list', { name: 'Verdicts' })
    const rows = within(list).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent('agent qa-explorer for bob')
    expect(rows[0]).toHaveTextContent('run 1, attempt 1')
    expect(rows[0]).toHaveTextContent('1 finding')
    expect(within(rows[0]).getByRole('link', { name: 'Evidence from qa-explorer (opens in a new tab)' }))
      .toHaveAttribute('href', 'https://ci.example.com/1')
    // The verifier is an actor badge when the gate knows who wrote it, else the sentence.
    expect(rows[0].querySelector('[data-actor-kind="agent"]')).toHaveTextContent('qa-explorer for bob')
    expect(rows[1].querySelector('[data-actor-kind]')).toBeNull()
    expect(within(rows[0]).getByText('Pass')).toHaveClass('k-chip--verified')
    expect(rows[1]).toHaveTextContent("the author's party")
    expect(screen.getByText(/kardinal verify checkout uat --check exploratory-qa --pass/)).toHaveTextContent('-n payments')
  })
  it('links only http(s) evidence, and says when there is none yet', () => {
    render(<VerifyQuorum verify={{ ...waiting, verdicts: [{ name: 'x', verifier: 'bob', result: 'fail', counted: true,
      evidenceURL: 'javascript:alert(1)' }] }} now={now} />)
    expect(screen.queryByRole('link')).toBeNull()
  })
  it('shows an empty gate', () => {
    render(<VerifyQuorum verify={{ ...waiting, passed: 0, verdicts: [] }} now={now} />)
    expect(screen.getByText('No verdicts yet.')).toBeInTheDocument()
  })
  it('says what an inconclusive verdict does, and counts the ones that wait', () => {
    expect(verdictChip('inconclusive', 'hold')).toEqual({ tone: 'waiting', label: 'Inconclusive (holds)', glyph: '?' })
    expect(verdictChip('inconclusive', 'retry').label).toBe('Inconclusive (retries)')
    expect(verdictChip('inconclusive', 'fail')).toMatchObject({ tone: 'failed', label: 'Inconclusive (counts as a fail)' })
    expect(verdictChip('fail', 'hold').tone).toBe('failed')
    const inc: GateVerify = { ...waiting, verdicts: [...waiting.verdicts!, { name: 'r2-a1', verifier: 'agent qa-explorer for bob',
      result: 'inconclusive', counted: false, reason: 'inconclusive (retrying: attempt 2 of 2)', findings: 3, highFindings: 2 }] }
    expect(inconclusiveWaiting(inc)).toBe(1)
    expect(verifySummary(inc)).toBe('1 of 2 verdicts for exploratory-qa; 1 inconclusive, retrying')
    expect(inconclusiveWaiting({ ...inc, verdicts: [{ ...inc.verdicts![2], counted: true }] })).toBe(0)
    render(<VerifyQuorum verify={inc} now={now} />)
    const row = screen.getAllByRole('listitem')[2]
    expect(row).toHaveTextContent('Inconclusive (retries)')
    expect(row).toHaveTextContent('attempt 2 of 2')
    expect(row).toHaveTextContent('3 findings, 2 high')
  })
})
