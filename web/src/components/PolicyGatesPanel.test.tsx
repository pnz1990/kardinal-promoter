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

// PolicyGatesPanel.test.tsx — Tests for the policy gates panel (#533).
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { PolicyGatesPanel } from './PolicyGatesPanel'
import type { PolicyGate } from '../types'

const makeGate = (overrides: Partial<PolicyGate> = {}): PolicyGate => ({
  name: 'test-gate',
  namespace: 'default',
  expression: '!schedule.isWeekend()',
  ready: true,
  ...overrides,
})

describe('PolicyGatesPanel — empty state', () => {
  it('renders nothing when gates=[]', () => {
    const { container } = render(<PolicyGatesPanel gates={[]} />)
    expect(container.firstChild).toBeNull()
  })

  it('renders skeleton loading state (not text) when loading=true', () => {
    const { container } = render(<PolicyGatesPanel gates={[]} loading />)
    // Must NOT show the old text-based loading indicator
    expect(screen.queryByText(/loading/i)).not.toBeInTheDocument()
    // Must show the skeleton container with aria-busy
    const skeleton = container.querySelector('[aria-busy="true"]')
    expect(skeleton).toBeInTheDocument()
  })
})

describe('PolicyGatesPanel — collapsed by default when all pass', () => {
  it('starts collapsed when no gates are blocked', () => {
    const gates = [makeGate({ ready: true })]
    render(<PolicyGatesPanel gates={gates} />)
    const btn = screen.getByRole('button', { name: /Policy Gates/i })
    expect(btn).toHaveAttribute('aria-expanded', 'false')
    // Gate expression should not be visible
    expect(screen.queryByText('!schedule.isWeekend()')).not.toBeInTheDocument()
  })
})

describe('PolicyGatesPanel — auto-expand when blocked (#524)', () => {
  it('starts expanded when any gate is blocked', () => {
    const gates = [makeGate({ ready: false })]
    render(<PolicyGatesPanel gates={gates} />)
    const btn = screen.getByRole('button', { name: /Policy Gates/i })
    expect(btn).toHaveAttribute('aria-expanded', 'true')
  })

  it('shows gate expression when auto-expanded due to block', () => {
    const gates = [makeGate({ ready: false, expression: '!schedule.isWeekend()' })]
    render(<PolicyGatesPanel gates={gates} />)
    expect(screen.getByText('!schedule.isWeekend()')).toBeInTheDocument()
  })

  it('shows block reason when gate is blocked', () => {
    const gates = [makeGate({ ready: false, reason: 'It is Saturday' })]
    render(<PolicyGatesPanel gates={gates} />)
    expect(screen.getByText('It is Saturday')).toBeInTheDocument()
  })
})

describe('PolicyGatesPanel — toggle and content', () => {
  it('expands when toggle button is clicked', async () => {
    const user = userEvent.setup()
    const gates = [makeGate({ ready: true, expression: '!schedule.isWeekend()' })]
    render(<PolicyGatesPanel gates={gates} />)
    const btn = screen.getByRole('button', { name: /Policy Gates/i })
    await user.click(btn)
    expect(screen.getByText('!schedule.isWeekend()')).toBeInTheDocument()
    expect(btn).toHaveAttribute('aria-expanded', 'true')
  })

  it('collapses when toggle button clicked again', async () => {
    const user = userEvent.setup()
    const gates = [makeGate({ ready: false })]
    render(<PolicyGatesPanel gates={gates} />)
    const btn = screen.getByRole('button', { name: /Policy Gates/i })
    // initially expanded (blocked gate)
    await user.click(btn)
    expect(btn).toHaveAttribute('aria-expanded', 'false')
  })

  it('shows gate count in button label', () => {
    const gates = [makeGate(), makeGate({ name: 'gate-2' })]
    render(<PolicyGatesPanel gates={gates} />)
    expect(screen.getByText(/Policy Gates \(2\)/i)).toBeInTheDocument()
  })

  it('shows "X blocked" in summary chip', () => {
    const gates = [makeGate({ ready: false }), makeGate({ name: 'g2', ready: true })]
    render(<PolicyGatesPanel gates={gates} />)
    expect(screen.getByText('1 blocked')).toBeInTheDocument()
  })

  it('shows "X passing" when all gates pass', () => {
    const gates = [makeGate(), makeGate({ name: 'gate-2' })]
    render(<PolicyGatesPanel gates={gates} />)
    expect(screen.getByText('2 passing')).toBeInTheDocument()
  })
})
