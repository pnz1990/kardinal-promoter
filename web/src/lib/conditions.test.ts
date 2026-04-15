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

// lib/conditions.test.ts — Tests for the conditions helper (#529).
import { describe, it, expect } from 'vitest'
import {
  isHealthyCondition,
  conditionStatusLabel,
  conditionSortOrder,
  sortConditions,
  conditionsSummary,
  type Condition,
} from './conditions'

describe('isHealthyCondition', () => {
  it('Ready=True is healthy', () => {
    expect(isHealthyCondition('Ready', 'True')).toBe(true)
  })

  it('Ready=False is unhealthy', () => {
    expect(isHealthyCondition('Ready', 'False')).toBe(false)
  })

  it('Ready=Unknown is unhealthy', () => {
    expect(isHealthyCondition('Ready', 'Unknown')).toBe(false)
  })

  it('Degraded=True is unhealthy (inverted)', () => {
    expect(isHealthyCondition('Degraded', 'True')).toBe(false)
  })

  it('Degraded=False is healthy (inverted)', () => {
    expect(isHealthyCondition('Degraded', 'False')).toBe(true)
  })

  it('ReconciliationSuspended=True is unhealthy (inverted)', () => {
    expect(isHealthyCondition('ReconciliationSuspended', 'True')).toBe(false)
  })

  it('ReconciliationSuspended=False is healthy (inverted)', () => {
    expect(isHealthyCondition('ReconciliationSuspended', 'False')).toBe(true)
  })

  it('Paused=True is unhealthy (inverted)', () => {
    expect(isHealthyCondition('Paused', 'True')).toBe(false)
  })
})

describe('conditionStatusLabel', () => {
  it('healthy → ✓', () => {
    expect(conditionStatusLabel('Ready', 'True')).toBe('✓')
  })

  it('unhealthy → ✗', () => {
    expect(conditionStatusLabel('Ready', 'False')).toBe('✗')
  })

  it('unknown → ?', () => {
    expect(conditionStatusLabel('Ready', 'Unknown')).toBe('?')
  })

  it('inverted healthy → ✓', () => {
    expect(conditionStatusLabel('Degraded', 'False')).toBe('✓')
  })

  it('inverted unhealthy → ✗', () => {
    expect(conditionStatusLabel('Degraded', 'True')).toBe('✗')
  })
})

describe('conditionSortOrder', () => {
  it('unhealthy sorts first (0)', () => {
    expect(conditionSortOrder('Ready', 'False')).toBe(0)
  })

  it('unknown sorts second (1)', () => {
    expect(conditionSortOrder('Ready', 'Unknown')).toBe(1)
  })

  it('healthy sorts last (2)', () => {
    expect(conditionSortOrder('Ready', 'True')).toBe(2)
  })
})

describe('sortConditions', () => {
  it('failing conditions come first', () => {
    const conditions: Condition[] = [
      { type: 'Ready', status: 'True' },
      { type: 'Synced', status: 'False' },
      { type: 'Available', status: 'True' },
    ]
    const sorted = sortConditions(conditions)
    expect(sorted[0].type).toBe('Synced')
    expect(sorted[0].status).toBe('False')
  })

  it('does not mutate original array', () => {
    const conditions: Condition[] = [
      { type: 'Ready', status: 'True' },
      { type: 'Synced', status: 'False' },
    ]
    const original = [...conditions]
    sortConditions(conditions)
    expect(conditions).toEqual(original)
  })
})

describe('conditionsSummary', () => {
  it('counts healthy and total correctly', () => {
    const conditions: Condition[] = [
      { type: 'Ready', status: 'True' },
      { type: 'Synced', status: 'True' },
      { type: 'Available', status: 'False' },
    ]
    const { healthy, total } = conditionsSummary(conditions)
    expect(healthy).toBe(2)
    expect(total).toBe(3)
  })

  it('all healthy', () => {
    const conditions: Condition[] = [
      { type: 'Ready', status: 'True' },
    ]
    expect(conditionsSummary(conditions)).toEqual({ healthy: 1, total: 1 })
  })

  it('all unhealthy', () => {
    const conditions: Condition[] = [
      { type: 'Ready', status: 'False' },
      { type: 'Synced', status: 'False' },
    ]
    expect(conditionsSummary(conditions)).toEqual({ healthy: 0, total: 2 })
  })

  it('inverted condition counted correctly', () => {
    const conditions: Condition[] = [
      { type: 'Degraded', status: 'False' }, // healthy (inverted)
      { type: 'Ready', status: 'True' },      // healthy
    ]
    expect(conditionsSummary(conditions)).toEqual({ healthy: 2, total: 2 })
  })
})
