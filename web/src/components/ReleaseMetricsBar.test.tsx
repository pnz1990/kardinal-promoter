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

// components/ReleaseMetricsBar.test.tsx — release efficiency metrics (#465, C10b-web-10).
// Bundles use the shape the Go API returns: provenance.rollbackOf for rollbacks
// and status.environments[].healthCheckedAt for when an environment was verified.
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { computeReleaseMetrics, formatHours, ReleaseMetricsBar } from './ReleaseMetricsBar'
import type { Bundle } from '../types'

const HOUR = 3_600_000
const created = Date.parse('2026-03-01T00:00:00Z')

/** A bundle created `ageDays` before the reference time that reached prod `ttpHours` after creation. */
function bundle(name: string, opts: { ageDays?: number; ttpHours?: number; rollbackOf?: string } = {}): Bundle {
  const c = created - (opts.ageDays ?? 0) * 24 * HOUR
  return {
    name,
    namespace: 'default',
    phase: 'Promoting', // Bundle phase does not reach Verified; the metrics must not depend on it.
    type: 'image',
    pipeline: 'my-app',
    createdAt: new Date(c).toISOString(),
    provenance: opts.rollbackOf ? { rollbackOf: opts.rollbackOf } : undefined,
    environments: [
      { name: 'test', phase: 'Verified', healthCheckedAt: new Date(c + 0.1 * HOUR).toISOString() },
      ...(opts.ttpHours !== undefined
        ? [{ name: 'prod', phase: 'Verified', healthCheckedAt: new Date(c + opts.ttpHours * HOUR).toISOString() }]
        : []),
    ],
  }
}

describe('computeReleaseMetrics', () => {
  it.each([
    { name: 'no final environment known', bundles: [bundle('a', { ttpHours: 1 })], env: undefined },
    { name: 'no bundle reached the final environment', bundles: [bundle('a'), bundle('b')], env: 'prod' },
    { name: 'no bundles', bundles: [], env: 'prod' },
  ])('returns null when $name', ({ bundles, env }) => {
    expect(computeReleaseMetrics(bundles, env)).toBeNull()
  })

  it('measures time to prod from healthCheckedAt, not from bundle age', () => {
    // 30-day-old bundles that reached prod 1h and 3h after creation.
    const m = computeReleaseMetrics([bundle('a', { ageDays: 30, ttpHours: 1 }), bundle('b', { ageDays: 31, ttpHours: 3 })], 'prod')
    expect(m?.meanTtpHours).toBe(2)
  })

  it('counts rollbacks from provenance.rollbackOf', () => {
    const m = computeReleaseMetrics([
      bundle('v1', { ttpHours: 1 }),
      bundle('v2', { ttpHours: 1 }),
      bundle('v3'),
      bundle('v4', { rollbackOf: 'v3', ttpHours: 1 }),
      bundle('v5', { rollbackOf: 'v2' }),
    ], 'prod')
    expect(m).toMatchObject({ totalBundles: 5, rollbackCount: 2, rollbackRatePct: 40 })
  })

  it('counts deploys that reached the final environment, not the window size', () => {
    const m = computeReleaseMetrics([bundle('a', { ttpHours: 1 }), bundle('b'), bundle('c'), bundle('d', { ttpHours: 2 })], 'prod')
    expect(m).toMatchObject({ totalBundles: 4, deployCount: 2 })
  })

  it('covers the 10 newest bundles only', () => {
    const bundles = Array.from({ length: 12 }, (_, i) => bundle(`b${i}`, { ageDays: i, ttpHours: i < 10 ? 1 : 100 }))
    const m = computeReleaseMetrics(bundles, 'prod')
    expect(m).toMatchObject({ totalBundles: 10, deployCount: 10, meanTtpHours: 1 })
  })

  it('does not mutate the input array', () => {
    const bundles = [bundle('old', { ageDays: 2, ttpHours: 1 }), bundle('new', { ageDays: 1, ttpHours: 1 })]
    computeReleaseMetrics(bundles, 'prod')
    expect(bundles.map(b => b.name)).toEqual(['old', 'new'])
  })
})

describe('formatHours', () => {
  it.each([
    { hours: 0.4, want: '< 1h' },
    { hours: 2.4, want: '2h' },
    { hours: 72, want: '3d' },
  ])('$hours → $want', ({ hours, want }) => {
    expect(formatHours(hours)).toBe(want)
  })
})

describe('ReleaseMetricsBar', () => {
  it('renders nothing until a bundle has reached the final environment', () => {
    const { container } = render(<ReleaseMetricsBar bundles={[bundle('a')]} finalEnvironment="prod" />)
    expect(container).toBeEmptyDOMElement()
  })

  it('shows the three metrics named after the final environment', () => {
    render(
      <ReleaseMetricsBar
        bundles={[bundle('a', { ttpHours: 2 }), bundle('b', { rollbackOf: 'a', ttpHours: 4 }), bundle('c')]}
        finalEnvironment="prod"
      />,
    )
    const bar = screen.getByRole('region', { name: 'Release metrics' })
    expect(bar).toHaveTextContent('Time to prod3h')
    expect(bar).toHaveTextContent('Rollback rate33%1 rollback')
    expect(bar).toHaveTextContent('Deploys to prod2last 3 bundles')
  })
})
