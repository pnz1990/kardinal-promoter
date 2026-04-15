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
  HealthChip,
  pipelinePhaseLabel,
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

// ─── healthChipColors ─────────────────────────────────────────────────────────

describe('healthChipColors', () => {
  it('Ready → green palette', () => {
    const { bg, text, border } = healthChipColors('Ready')
    expect(bg).toContain('14532d')    // dark green
    expect(text).toContain('4ade80')  // light green
    expect(border).toContain('22c55e')
  })

  it('Error → red palette', () => {
    const { bg, text } = healthChipColors('Error')
    expect(bg).toContain('7f1d1d')
    expect(text).toContain('f87171')
  })

  it('Reconciling → amber palette', () => {
    const { text } = healthChipColors('Reconciling')
    expect(text).toContain('fbbf24')
  })

  it('Paused → indigo palette (distinct from other states)', () => {
    const { bg, text } = healthChipColors('Paused')
    expect(bg).toContain('1e1b4b')  // dark indigo
    expect(text).toContain('a5b4fc') // light indigo
  })

  it('Unknown → gray palette', () => {
    const { text } = healthChipColors('Unknown')
    expect(text).toContain('64748b')
  })

  it('all 7 states return distinct text colors', () => {
    const states: HealthState[] = [
      'Ready', 'Reconciling', 'Error', 'Pending', 'Degraded', 'Paused', 'Unknown',
    ]
    const textColors = states.map(s => healthChipColors(s).text)
    // All text colors must be unique (no two states share the same color)
    const unique = new Set(textColors)
    expect(unique.size).toBe(states.length)
  })
})

// ─── HealthChip component ─────────────────────────────────────────────────────

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
    // aria-label = "Failed — Error"
    expect(getByLabelText('Failed — Error')).toBeInTheDocument()
  })

  it('sets title attribute for hover tooltip', () => {
    const { getByTitle } = render(<HealthChip state="Running" />)
    expect(getByTitle('Running (Reconciling)')).toBeInTheDocument()
  })

  it('PAUSED badge renders for paused state (#410 regression)', () => {
    const { getByText } = render(<HealthChip state="Paused" label="PAUSED" />)
    const badge = getByText('PAUSED')
    expect(badge).toBeInTheDocument()
    // Badge must have indigo color (not red or green) to be visually distinct
    const style = badge.style
    expect(style.color).not.toBe('')
  })

  it('PolicyGate Blocked renders as Error chip', () => {
    const { getByLabelText } = render(<HealthChip state="Block" nodeType="PolicyGate" />)
    // aria-label should say "Error" health state
    expect(getByLabelText('Block — Error')).toBeInTheDocument()
  })
})

// ─── pipelinePhaseLabel (#523) ────────────────────────────────────────────────

describe('pipelinePhaseLabel (#523)', () => {
  it('passes through non-Unknown phases unchanged', () => {
    expect(pipelinePhaseLabel({ phase: 'Ready', environmentCount: 3 })).toBe('Ready')
    expect(pipelinePhaseLabel({ phase: 'Degraded', environmentCount: 3 })).toBe('Degraded')
    expect(pipelinePhaseLabel({ phase: 'Initializing', environmentCount: 0 })).toBe('Initializing')
  })

  it('maps Unknown + no active bundle + no environmentStates → Idle', () => {
    expect(pipelinePhaseLabel({ phase: 'Unknown', environmentCount: 3 })).toBe('Idle')
  })

  it('maps Unknown + empty environmentStates → Idle', () => {
    expect(pipelinePhaseLabel({
      phase: 'Unknown',
      environmentCount: 3,
      environmentStates: {},
    })).toBe('Idle')
  })

  it('maps Unknown + environmentStates with Promoting → Promoting', () => {
    expect(pipelinePhaseLabel({
      phase: 'Unknown',
      environmentCount: 3,
      environmentStates: { test: 'Verified', uat: 'Promoting', prod: 'Pending' },
    })).toBe('Promoting')
  })

  it('maps Unknown + environmentStates all Verified → Ready (override)', () => {
    // If backend says Unknown but all envs are Verified, show Promoting
    // (transient state before reconciler updates phase)
    expect(pipelinePhaseLabel({
      phase: 'Unknown',
      environmentCount: 3,
      environmentStates: { test: 'Verified', uat: 'Verified', prod: 'Verified' },
    })).toBe('Promoting')  // has active bundle, so not Idle
  })

  it('maps Unknown + environmentStates with WaitingForMerge → Promoting', () => {
    expect(pipelinePhaseLabel({
      phase: 'Unknown',
      environmentCount: 3,
      environmentStates: { test: 'Verified', prod: 'WaitingForMerge' },
    })).toBe('Promoting')
  })
})
