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

// components/ReleaseMetricsBar.test.tsx — Tests for #465 release efficiency metrics.
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import {
  computeReleaseMetrics,
  ReleaseMetricsBar,
} from './ReleaseMetricsBar'
import type { Bundle } from '../types'

// ─── Test helpers ──────────────────────────────────────────────────────────────

function makeBundle(overrides: Partial<Bundle> = {}): Bundle {
  return {
    name: 'bundle-v1',
    namespace: 'default',
    phase: 'Verified',
    type: 'image',
    pipeline: 'my-app',
    createdAt: new Date(Date.now() - 60 * 60 * 1000).toISOString(), // 1h ago
    ...overrides,
  }
}

// ─── computeReleaseMetrics ────────────────────────────────────────────────────

describe('computeReleaseMetrics', () => {
  it('returns null when fewer than 5 bundles', () => {
    const bundles = [makeBundle(), makeBundle({ name: 'v2' })]
    expect(computeReleaseMetrics(bundles)).toBeNull()
  })

  it('returns metrics when 5+ bundles provided', () => {
    const bundles = Array.from({ length: 5 }, (_, i) =>
      makeBundle({ name: `bundle-v${i}` })
    )
    const metrics = computeReleaseMetrics(bundles)
    expect(metrics).not.toBeNull()
    expect(metrics!.rollbackRatePct).toBeGreaterThanOrEqual(0)
    expect(metrics!.rollbackRatePct).toBeLessThanOrEqual(100)
  })

  it('rollback rate is 0 when no rollbacks', () => {
    const bundles = Array.from({ length: 5 }, (_, i) =>
      makeBundle({ name: `bundle-v${i}`, isRollback: false })
    )
    const metrics = computeReleaseMetrics(bundles)
    expect(metrics!.rollbackRatePct).toBe(0)
  })

  it('rollback rate is 40% when 2 of 5 are rollbacks', () => {
    const bundles = [
      makeBundle({ name: 'v1', isRollback: false }),
      makeBundle({ name: 'v2', isRollback: false }),
      makeBundle({ name: 'v3', isRollback: false }),
      makeBundle({ name: 'v4', isRollback: true }),
      makeBundle({ name: 'v5', isRollback: true }),
    ]
    const metrics = computeReleaseMetrics(bundles)
    expect(metrics!.rollbackRatePct).toBe(40)
  })

  it('rollback rate is 100% when all bundles are rollbacks', () => {
    const bundles = Array.from({ length: 5 }, (_, i) =>
      makeBundle({ name: `bundle-v${i}`, isRollback: true })
    )
    const metrics = computeReleaseMetrics(bundles)
    expect(metrics!.rollbackRatePct).toBe(100)
  })

  it('does not mutate the original array', () => {
    const bundles = Array.from({ length: 5 }, (_, i) =>
      makeBundle({ name: `bundle-v${i}` })
    )
    const original = bundles.map(b => b.name)
    computeReleaseMetrics(bundles)
    expect(bundles.map(b => b.name)).toEqual(original)
  })
})

// ─── ReleaseMetricsBar component ─────────────────────────────────────────────

describe('ReleaseMetricsBar', () => {
  it('shows "Not enough data" when fewer than 5 bundles', () => {
    const bundles = [makeBundle(), makeBundle({ name: 'v2' })]
    render(<ReleaseMetricsBar bundles={bundles} />)
    expect(screen.getByText(/not enough data/i)).toBeInTheDocument()
  })

  it('shows rollback rate when 5+ bundles', () => {
    const bundles = Array.from({ length: 5 }, (_, i) =>
      makeBundle({ name: `bundle-v${i}`, isRollback: i === 0 })
    )
    render(<ReleaseMetricsBar bundles={bundles} />)
    // "Rollback Rate" label and "1 rollbacks" sub-label both match /rollback/i — use getAllByText
    expect(screen.getAllByText(/rollback/i).length).toBeGreaterThan(0)
    expect(screen.getByText(/20%/)).toBeInTheDocument()
  })

  it('renders all three metric labels', () => {
    const bundles = Array.from({ length: 5 }, (_, i) =>
      makeBundle({ name: `bundle-v${i}` })
    )
    render(<ReleaseMetricsBar bundles={bundles} />)
    expect(screen.getByText(/time to prod/i)).toBeInTheDocument()
    // "Rollback Rate" label and "X rollbacks" sub-text both match — use getAllByText
    expect(screen.getAllByText(/rollback/i).length).toBeGreaterThan(0)
    expect(screen.getByText(/deploys/i)).toBeInTheDocument()
  })

  it('shows empty state with guidance text', () => {
    render(<ReleaseMetricsBar bundles={[]} />)
    expect(screen.getByText(/not enough data/i)).toBeInTheDocument()
    expect(screen.getByText(/5\+/)).toBeInTheDocument()
  })
})
