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

// GateDetailPanel.test.tsx — Tests for #502 policy gate detail panel.
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import type { GraphNode, PolicyGate } from '../types'
import {
  GateDetailPanel,
  tokenizeCEL,
  relativeTime,
  blockingDuration,
  isOverrideExpired,
} from './GateDetailPanel'

// ─── Fixtures ─────────────────────────────────────────────────────────────────

function makeNode(overrides: Partial<GraphNode> = {}): GraphNode {
  return {
    id: 'no-weekend-deploys',
    type: 'PolicyGate',
    label: 'no-weekend-deploys',
    environment: 'prod',
    state: 'Block',
    expression: '!schedule.isWeekend()',
    ...overrides,
  }
}

function makeGate(overrides: Partial<PolicyGate> = {}): PolicyGate {
  return {
    name: 'no-weekend-deploys',
    namespace: 'platform-policies',
    expression: '!schedule.isWeekend()',
    ready: false,
    reason: 'Weekend deploy blocked',
    ...overrides,
  }
}

// ─── tokenizeCEL ──────────────────────────────────────────────────────────────

describe('tokenizeCEL', () => {
  it('tokenizes keywords as keyword type', () => {
    const tokens = tokenizeCEL('true && false')
    const types = tokens.filter(t => t.text.trim()).map(t => t.type)
    expect(types).toContain('keyword')
  })

  it('tokenizes string literals', () => {
    const tokens = tokenizeCEL('"hello"')
    expect(tokens[0].type).toBe('string')
    expect(tokens[0].text).toBe('"hello"')
  })

  it('tokenizes numbers', () => {
    const tokens = tokenizeCEL('42')
    expect(tokens[0].type).toBe('number')
  })

  it('tokenizes operators', () => {
    const tokens = tokenizeCEL('>=')
    expect(tokens.find(t => t.text === '>=')?.type).toBe('operator')
  })

  it('handles empty string', () => {
    expect(tokenizeCEL('')).toHaveLength(0)
  })

  it('handles complex expression', () => {
    const tokens = tokenizeCEL('!schedule.isWeekend() && bundle.upstreamSoakMinutes >= 30')
    // Should have multiple tokens, not throw
    expect(tokens.length).toBeGreaterThan(0)
  })
})

// ─── relativeTime ─────────────────────────────────────────────────────────────

describe('relativeTime', () => {
  it('returns — for undefined', () => {
    expect(relativeTime(undefined)).toBe('—')
  })

  it('returns — for invalid date', () => {
    expect(relativeTime('not-a-date')).toBe('—')
  })

  it('formats seconds ago', () => {
    const iso = new Date(Date.now() - 30000).toISOString()
    expect(relativeTime(iso)).toBe('30s ago')
  })

  it('formats minutes ago', () => {
    const iso = new Date(Date.now() - 5 * 60 * 1000).toISOString()
    expect(relativeTime(iso)).toBe('5m ago')
  })

  it('formats hours ago', () => {
    const iso = new Date(Date.now() - 3 * 3600 * 1000).toISOString()
    expect(relativeTime(iso)).toBe('3h ago')
  })

  it('formats days ago', () => {
    const iso = new Date(Date.now() - 2 * 86400 * 1000).toISOString()
    expect(relativeTime(iso)).toBe('2d ago')
  })
})

// ─── blockingDuration ─────────────────────────────────────────────────────────

describe('blockingDuration', () => {
  it('returns null when not blocking', () => {
    const iso = new Date(Date.now() - 60000).toISOString()
    expect(blockingDuration(iso, false)).toBeNull()
  })

  it('returns null when no lastEvaluatedAt', () => {
    expect(blockingDuration(undefined, true)).toBeNull()
  })

  it('returns blocking message for < 1 minute', () => {
    const iso = new Date(Date.now() - 30000).toISOString()
    expect(blockingDuration(iso, true)).toBe('blocking for < 1 minute')
  })

  it('returns blocking message for 5 minutes', () => {
    const iso = new Date(Date.now() - 5 * 60 * 1000).toISOString()
    expect(blockingDuration(iso, true)).toBe('blocking for 5 minutes')
  })

  it('uses singular for 1 minute', () => {
    const iso = new Date(Date.now() - 90 * 1000).toISOString()
    expect(blockingDuration(iso, true)).toBe('blocking for 1 minute')
  })
})

// ─── isOverrideExpired ────────────────────────────────────────────────────────

describe('isOverrideExpired', () => {
  it('returns false for future expiry', () => {
    const future = new Date(Date.now() + 3600000).toISOString()
    expect(isOverrideExpired(future)).toBe(false)
  })

  it('returns true for past expiry', () => {
    const past = new Date(Date.now() - 3600000).toISOString()
    expect(isOverrideExpired(past)).toBe(true)
  })

  it('returns false for undefined', () => {
    expect(isOverrideExpired(undefined)).toBe(false)
  })
})

// ─── GateDetailPanel component ────────────────────────────────────────────────

describe('GateDetailPanel', () => {
  it('renders gate name in header', () => {
    const node = makeNode()
    render(<GateDetailPanel node={node} gates={[makeGate()]} onClose={() => {}} />)
    expect(screen.getByText('no-weekend-deploys')).toBeInTheDocument()
  })

  it('renders CEL expression with aria-label', () => {
    const node = makeNode({ expression: '!schedule.isWeekend()' })
    render(<GateDetailPanel node={node} gates={[makeGate()]} onClose={() => {}} />)
    expect(screen.getByLabelText('CEL expression')).toBeInTheDocument()
    expect(screen.getByLabelText('CEL expression').textContent).toContain('isWeekend')
  })

  it('shows status reason when gate is found', () => {
    const node = makeNode()
    const gate = makeGate({ reason: 'Weekend deploy blocked' })
    render(<GateDetailPanel node={node} gates={[gate]} onClose={() => {}} />)
    expect(screen.getByText('Weekend deploy blocked')).toBeInTheDocument()
  })

  it('shows evaluated time when lastEvaluatedAt is set', () => {
    const node = makeNode()
    const past5m = new Date(Date.now() - 5 * 60 * 1000).toISOString()
    const gate = makeGate({ lastEvaluatedAt: past5m })
    render(<GateDetailPanel node={node} gates={[gate]} onClose={() => {}} />)
    expect(screen.getByText(/evaluated 5m ago/)).toBeInTheDocument()
  })

  it('shows blocking duration when gate is not ready', () => {
    const node = makeNode()
    const past10m = new Date(Date.now() - 10 * 60 * 1000).toISOString()
    const gate = makeGate({ ready: false, lastEvaluatedAt: past10m })
    render(<GateDetailPanel node={node} gates={[gate]} onClose={() => {}} />)
    const blockingEl = screen.getByLabelText('blocking for 10 minutes')
    expect(blockingEl).toBeInTheDocument()
  })

  it('shows active override with reason', () => {
    const node = makeNode()
    const futureExpiry = new Date(Date.now() + 3600000).toISOString()
    const gate = makeGate({
      overrides: [{ reason: 'P0 incident #123', createdBy: 'alice', expiresAt: futureExpiry }],
    })
    render(<GateDetailPanel node={node} gates={[gate]} onClose={() => {}} />)
    expect(screen.getByText('P0 incident #123')).toBeInTheDocument()
    expect(screen.getByText('Active Overrides')).toBeInTheDocument()
  })

  it('shows expired override in history section', () => {
    const node = makeNode()
    const pastExpiry = new Date(Date.now() - 3600000).toISOString()
    const gate = makeGate({
      overrides: [{ reason: 'old override', expiresAt: pastExpiry }],
    })
    render(<GateDetailPanel node={node} gates={[gate]} onClose={() => {}} />)
    expect(screen.getByText('Override History')).toBeInTheDocument()
    expect(screen.getByText('old override')).toBeInTheDocument()
  })

  it('does not show override sections when no overrides', () => {
    const node = makeNode()
    const gate = makeGate({ overrides: [] })
    render(<GateDetailPanel node={node} gates={[gate]} onClose={() => {}} />)
    expect(screen.queryByText('Active Overrides')).not.toBeInTheDocument()
    expect(screen.queryByText('Override History')).not.toBeInTheDocument()
  })

  it('calls onClose when × is clicked', () => {
    const onClose = vi.fn()
    const node = makeNode()
    render(<GateDetailPanel node={node} gates={[makeGate()]} onClose={onClose} />)
    fireEvent.click(screen.getByLabelText('Close gate detail'))
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('calls onClose when Escape is pressed', () => {
    const onClose = vi.fn()
    const node = makeNode()
    render(<GateDetailPanel node={node} gates={[makeGate()]} onClose={onClose} />)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledOnce()
  })
})
