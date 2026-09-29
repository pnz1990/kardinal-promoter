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

import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import {
  kardinalStateToHealth,
  healthChipColors,
  healthStateClass,
  HealthChip,
  type HealthState,
} from './HealthChip'

// ─── kardinalStateToHealth ────────────────────────────────────────────────────

describe('kardinalStateToHealth', () => {
  it.each<[string, HealthState]>([
    // PromotionStep states (api/v1alpha1) and the graph API's NotStarted.
    ['Pending', 'Pending'],
    ['Promoting', 'Reconciling'],
    ['WaitingForMerge', 'Reconciling'],
    ['HealthChecking', 'Reconciling'],
    ['Verified', 'Ready'],
    ['Failed', 'Error'],
    ['AbortedByAlarm', 'Error'],
    ['RollingBack', 'Reconciling'],
    ['NotStarted', 'Pending'],
    // Bundle phases.
    ['Available', 'Pending'],
    ['Superseded', 'Unknown'],
    // Pipeline phases (DerivePhase) and the paused pseudo-state.
    ['Ready', 'Ready'],
    ['Degraded', 'Degraded'],
    ['Unknown', 'Unknown'],
    ['Paused', 'Paused'],
    // Gate states rendered without nodeType.
    ['Pass', 'Ready'],
    ['Block', 'Error'],
    ['SomeUnknownState', 'Unknown'],
  ])('maps %s → %s (default nodeType)', (state, expected) => {
    expect(kardinalStateToHealth(state)).toBe(expected)
  })

  describe('PolicyGate nodeType', () => {
    it.each<[string, HealthState]>([
      ['Pass', 'Ready'],
      ['Block', 'Error'],
      ['Fail', 'Error'],
      ['Pending', 'Pending'],
      ['SomeUnknown', 'Unknown'],
    ])('maps %s → %s', (state, expected) => {
      expect(kardinalStateToHealth(state, 'PolicyGate')).toBe(expected)
    })
  })
})

// ─── healthChipColors (retained for SVG use) ──────────────────────────────────

describe('healthChipColors', () => {
  const ALL: HealthState[] = ['Ready', 'Reconciling', 'Error', 'Pending', 'Degraded', 'Paused', 'Unknown']

  it.each<[HealthState, string, string]>([
    ['Ready', 'var(--color-success-bg)', 'var(--color-success)'],
    ['Reconciling', 'var(--color-warning-bg)', 'var(--color-warning)'],
    ['Error', 'var(--color-error-bg)', 'var(--color-error)'],
    ['Pending', 'var(--color-surface)', 'var(--color-text-muted)'],
    ['Degraded', 'var(--color-degraded-bg)', 'var(--color-degraded)'],
    ['Paused', 'var(--color-accent-bg)', 'var(--color-accent)'],
    ['Unknown', 'var(--color-surface)', 'var(--color-text-faint)'],
  ])('%s uses theme tokens (bg %s, text %s)', (state, bg, text) => {
    const c = healthChipColors(state)
    expect(c.bg).toBe(bg)
    expect(c.text).toBe(text)
    expect(c.border).toMatch(/^var\(--color-[a-z-]+\)$/)
  })

  // A hard-coded dark color made DAG nodes unreadable in light mode
  // (dark green fill behind light-mode dark-green text).
  it.each(ALL)('%s has no hard-coded color', state => {
    const c = healthChipColors(state)
    for (const v of [c.bg, c.text, c.border]) expect(v).not.toMatch(/#[0-9a-f]{3,8}/i)
  })

  it('all 7 states return distinct text colors', () => {
    const unique = new Set(ALL.map(s => healthChipColors(s).text))
    expect(unique.size).toBe(ALL.length)
  })
})

// ─── healthStateClass (#532) ──────────────────────────────────────────────────

describe('healthStateClass (#532)', () => {
  it.each<[HealthState, string]>([
    ['Ready', 'health-chip--ready'],
    ['Reconciling', 'health-chip--reconciling'],
    ['Error', 'health-chip--error'],
    ['Pending', 'health-chip--pending'],
    ['Degraded', 'health-chip--degraded'],
    ['Paused', 'health-chip--paused'],
    ['Unknown', 'health-chip--unknown'],
  ])('%s → %s', (state, expected) => {
    expect(healthStateClass(state)).toBe(expected)
  })
})

// ─── HealthChip component — CSS class assertions (#532) ──────────────────────

describe('HealthChip component', () => {
  it('renders the state label', () => {
    const { getByText } = render(<HealthChip state="Verified" />)
    expect(getByText('Verified')).toBeInTheDocument()
  })

  it('renders custom label when provided', () => {
    const { getByText } = render(<HealthChip state="Verified" label="All good" />)
    expect(getByText('All good')).toBeInTheDocument()
  })

  it('sets aria-label for screen readers', () => {
    const { getByLabelText } = render(<HealthChip state="Failed" />)
    expect(getByLabelText('Failed — Error')).toBeInTheDocument()
  })

  it('sets title attribute for hover tooltip', () => {
    const { getByTitle } = render(<HealthChip state="WaitingForMerge" />)
    expect(getByTitle('WaitingForMerge (Reconciling)')).toBeInTheDocument()
  })

  // #532: Assert CSS classes, NOT hex color strings
  it('Ready state has health-chip--ready CSS class', () => {
    const { getByLabelText } = render(<HealthChip state="Verified" />)
    const chip = getByLabelText('Verified — Ready')
    expect(chip).toHaveClass('health-chip')
    expect(chip).toHaveClass('health-chip--ready')
  })

  it('Reconciling state has health-chip--reconciling CSS class', () => {
    const { getByLabelText } = render(<HealthChip state="Promoting" />)
    const chip = getByLabelText('Promoting — Reconciling')
    expect(chip).toHaveClass('health-chip--reconciling')
  })

  it('Error state has health-chip--error CSS class', () => {
    const { getByLabelText } = render(<HealthChip state="Failed" />)
    expect(getByLabelText('Failed — Error')).toHaveClass('health-chip--error')
  })

  it('Pending state has health-chip--pending CSS class', () => {
    const { getByLabelText } = render(<HealthChip state="Pending" />)
    expect(getByLabelText('Pending — Pending')).toHaveClass('health-chip--pending')
  })

  it('Paused state has health-chip--paused CSS class', () => {
    const { getByLabelText } = render(<HealthChip state="Paused" />)
    expect(getByLabelText('Paused — Paused')).toHaveClass('health-chip--paused')
  })

  it('Unknown state has health-chip--unknown CSS class', () => {
    const { getByLabelText } = render(<HealthChip state="Superseded" />)
    expect(getByLabelText('Superseded — Unknown')).toHaveClass('health-chip--unknown')
  })

  it('sm size has health-chip--sm CSS class', () => {
    const { getByText } = render(<HealthChip state="Verified" size="sm" />)
    expect(getByText('Verified')).toHaveClass('health-chip--sm')
  })

  it('md size has health-chip--md CSS class', () => {
    const { getByText } = render(<HealthChip state="Verified" size="md" />)
    expect(getByText('Verified')).toHaveClass('health-chip--md')
  })

  it('has data-health-state attribute for testing/automation', () => {
    const { getByText } = render(<HealthChip state="Verified" />)
    expect(getByText('Verified')).toHaveAttribute('data-health-state', 'Ready')
  })

  it('PAUSED badge renders for paused state', () => {
    const { getByText } = render(<HealthChip state="Paused" label="PAUSED" />)
    const badge = getByText('PAUSED')
    expect(badge).toBeInTheDocument()
    expect(badge).toHaveClass('health-chip--paused')
  })

  it('PolicyGate Blocked renders as Error chip', () => {
    const { getByLabelText } = render(<HealthChip state="Block" nodeType="PolicyGate" />)
    const chip = getByLabelText('Block — Error')
    expect(chip).toHaveClass('health-chip--error')
  })
})
