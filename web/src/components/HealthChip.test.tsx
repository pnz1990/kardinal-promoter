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
    ['Succeeded', 'Ready'],
    ['Verified', 'Ready'],
    ['Pass', 'Ready'],
    ['Running', 'Reconciling'],
    ['Promoting', 'Reconciling'],
    ['WaitingForMerge', 'Reconciling'],
    ['HealthChecking', 'Reconciling'],
    ['Failed', 'Error'],
    ['Block', 'Error'],
    ['Pending', 'Pending'],
    ['Available', 'Pending'],
    ['Superseded', 'Unknown'],
    ['Paused', 'Paused'],
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
  it('Ready → green palette', () => {
    const { bg, text, border } = healthChipColors('Ready')
    expect(bg).toContain('14532d')    // dark green (not yet tokenized)
    expect(text).toContain('color-success')  // was #4ade80, now CSS var
    expect(border).toContain('22c55e')
  })

  it('Error → red palette', () => {
    const { bg, text } = healthChipColors('Error')
    expect(bg).toContain('7f1d1d')
    expect(text).toContain('color-error')  // was #f87171, now CSS var
  })

  it('Reconciling → amber palette', () => {
    const { text } = healthChipColors('Reconciling')
    expect(text).toContain('color-warning')  // was #fbbf24, now CSS var
  })

  it('Paused → indigo palette (distinct from other states)', () => {
    const { bg, text } = healthChipColors('Paused')
    expect(bg).toContain('1e1b4b')  // dark indigo (not yet tokenized)
    expect(text).toContain('color-accent') // was #a5b4fc, now CSS var
  })

  it('Unknown → gray palette', () => {
    const { text } = healthChipColors('Unknown')
    expect(text).toContain('color-text-muted')
  })

  it('all 7 states return distinct text colors', () => {
    const states: HealthState[] = [
      'Ready', 'Reconciling', 'Error', 'Pending', 'Degraded', 'Paused', 'Unknown',
    ]
    const textColors = states.map(s => healthChipColors(s).text)
    const unique = new Set(textColors)
    expect(unique.size).toBe(states.length)
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
    const { getByTitle } = render(<HealthChip state="Running" />)
    expect(getByTitle('Running (Reconciling)')).toBeInTheDocument()
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
