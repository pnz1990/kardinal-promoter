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

// PolicyGatesPanel.test.tsx — Tests for the policy gates panel (#524).
import { describe, it, expect } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { PolicyGatesPanel } from './PolicyGatesPanel'
import type { PolicyGate } from '../types'

const makeGate = (overrides: Partial<PolicyGate> = {}): PolicyGate => ({
  name: 'test-gate',
  namespace: 'default',
  expression: '!schedule.isWeekend()',
  ready: true,
  ...overrides,
})

describe('PolicyGatesPanel — collapsed by default when all gates pass', () => {
  it('starts collapsed when no gates are blocked', () => {
    const gates = [makeGate({ ready: true }), makeGate({ name: 'gate-2', ready: true })]
    render(<PolicyGatesPanel gates={gates} />)
    // Panel header should be visible
    expect(screen.getByRole('button', { name: /Policy Gates/i })).toBeInTheDocument()
    // Gate details should NOT be visible
    expect(screen.queryByText('!schedule.isWeekend()')).not.toBeInTheDocument()
  })

  it('has aria-expanded=false when collapsed', () => {
    const gates = [makeGate({ ready: true })]
    render(<PolicyGatesPanel gates={gates} />)
    const btn = screen.getByRole('button', { name: /Policy Gates/i })
    expect(btn).toHaveAttribute('aria-expanded', 'false')
  })
})

describe('PolicyGatesPanel — auto-expands when blocked (#524)', () => {
  it('starts expanded when at least one gate is blocked', () => {
    const gates = [
      makeGate({ name: 'gate-pass', ready: true }),
      makeGate({ name: 'gate-block', ready: false }),
    ]
    render(<PolicyGatesPanel gates={gates} />)
    // Panel should be expanded automatically
    const btn = screen.getByRole('button', { name: /Policy Gates/i })
    expect(btn).toHaveAttribute('aria-expanded', 'true')
  })

  it('shows gate details when auto-expanded due to blockage', () => {
    const gates = [
      makeGate({ name: 'blocked-gate', ready: false, expression: '!schedule.isWeekend()' }),
    ]
    render(<PolicyGatesPanel gates={gates} />)
    // Gate expression should be visible because panel auto-expanded
    expect(screen.getByText('!schedule.isWeekend()')).toBeInTheDocument()
  })

  it('shows block reason when gate is blocked', () => {
    const gates = [
      makeGate({ ready: false, reason: 'It is the weekend', name: 'no-weekend' }),
    ]
    render(<PolicyGatesPanel gates={gates} />)
    expect(screen.getByText('It is the weekend')).toBeInTheDocument()
  })
})

describe('PolicyGatesPanel — toggle behaviour', () => {
  it('click handler toggles open state', () => {
    const gates = [makeGate({ ready: true })]
    render(<PolicyGatesPanel gates={gates} />)
    const btn = screen.getByRole('button', { name: /Policy Gates/i })
    
    // Initially collapsed (all passing)
    expect(btn).toHaveAttribute('aria-expanded', 'false')
    
    // Click to expand
    fireEvent.click(btn)
    expect(btn).toHaveAttribute('aria-expanded', 'true')
    
    // Click to collapse
    fireEvent.click(btn)
    expect(btn).toHaveAttribute('aria-expanded', 'false')
  })

  it('renders nothing when gates array is empty', () => {
    const { container } = render(<PolicyGatesPanel gates={[]} />)
    expect(container.firstChild).toBeNull()
  })
})

describe('PolicyGatesPanel — summary chip', () => {
  it('shows "X blocked" when gates are blocked', () => {
    const gates = [
      makeGate({ ready: false }),
      makeGate({ name: 'gate-2', ready: false }),
      makeGate({ name: 'gate-3', ready: true }),
    ]
    render(<PolicyGatesPanel gates={gates} />)
    expect(screen.getByText('2 blocked')).toBeInTheDocument()
  })

  it('shows "X passing" when all gates pass', () => {
    const gates = [makeGate(), makeGate({ name: 'gate-2' })]
    render(<PolicyGatesPanel gates={gates} />)
    expect(screen.getByText('2 passing')).toBeInTheDocument()
  })
})
